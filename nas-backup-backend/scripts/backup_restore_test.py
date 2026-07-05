#!/usr/bin/env python3
"""
NAS Backup System - End-to-End Closed-Loop Test Script

Tests the complete backup-upload-restore-verify cycle for a specified file:
  1. Calculate original file SHA256 hash
  2. Check OSS storage health
  3. Trigger a full/incremental backup
  4. Wait for backup to complete (polling status)
  5. Restore the specified file to a temp directory
  6. Calculate restored file SHA256 hash
  7. Compare hashes to verify data integrity

Usage:
  python3 backup_restore_test.py --api http://localhost:8080 --file /path/to/test/file.txt
  python3 backup_restore_test.py --api http://localhost:8080 --file /data/test.bin --type full --keep-artifacts
"""

import argparse
import hashlib
import json
import os
import shutil
import sys
import tempfile
import time
from typing import Optional, Dict, Any

requests = None


class Colors:
    GREEN = "\033[92m"
    RED = "\033[91m"
    YELLOW = "\033[93m"
    BLUE = "\033[94m"
    BOLD = "\033[1m"
    RESET = "\033[0m"


def log_info(msg: str):
    print(f"{Colors.BLUE}[INFO]{Colors.RESET} {msg}")


def log_success(msg: str):
    print(f"{Colors.GREEN}[PASS]{Colors.RESET} {msg}")


def log_fail(msg: str):
    print(f"{Colors.RED}[FAIL]{Colors.RESET} {msg}")


def log_warn(msg: str):
    print(f"{Colors.YELLOW}[WARN]{Colors.RESET} {msg}")


def log_step(msg: str):
    print(f"\n{Colors.BOLD}=== {msg} ==={Colors.RESET}")


def sha256_file(filepath: str) -> str:
    h = hashlib.sha256()
    with open(filepath, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def api_get(base_url: str, path: str, timeout: int = 30) -> Dict[str, Any]:
    url = f"{base_url.rstrip('/')}{path}"
    resp = requests.get(url, timeout=timeout)
    resp.raise_for_status()
    data = resp.json()
    if not data.get("success", False):
        raise RuntimeError(f"API error: {data.get('error', 'unknown')}")
    return data.get("data", {})


def api_post(base_url: str, path: str, body: Optional[Dict] = None, timeout: int = 60) -> Dict[str, Any]:
    url = f"{base_url.rstrip('/')}{path}"
    headers = {"Content-Type": "application/json"}
    resp = requests.post(url, json=body, headers=headers, timeout=timeout)
    resp.raise_for_status()
    data = resp.json()
    if not data.get("success", False):
        raise RuntimeError(f"API error: {data.get('error', 'unknown')} (HTTP {resp.status_code})")
    return data.get("data", {})


def wait_for_backup_completion(
    base_url: str,
    backup_id: Optional[int],
    timeout_sec: int = 3600,
    poll_interval: float = 2.0,
) -> Dict[str, Any]:
    log_info(f"Waiting for backup to complete (timeout: {timeout_sec}s)...")
    start = time.time()
    last_phase = None
    last_percent = -1

    while time.time() - start < timeout_sec:
        try:
            status = api_get(base_url, "/api/backup/status")
        except Exception as e:
            log_warn(f"Status poll failed: {e}, retrying...")
            time.sleep(poll_interval)
            continue

        is_running = status.get("is_running", False)
        running_backup = status.get("running_backup")

        if running_backup:
            rb_status = running_backup.get("status")
            rb_id = running_backup.get("id")
            uploaded = running_backup.get("uploaded_size", 0)
            total_size = running_backup.get("total_size", 0)

            if rb_status == "completed":
                log_info(f"Backup {rb_id} completed successfully")
                return running_backup
            elif rb_status == "failed":
                err = running_backup.get("error_message", "unknown error")
                raise RuntimeError(f"Backup {rb_id} failed: {err}")
            elif rb_status == "cancelled":
                raise RuntimeError(f"Backup {rb_id} was cancelled")

            if total_size > 0:
                percent = min(100.0, (uploaded / total_size) * 100)
                if int(percent) != int(last_percent):
                    log_info(f"  Backup {rb_id} status={rb_status}, progress={percent:.1f}% ({uploaded}/{total_size} bytes)")
                    last_percent = percent
            else:
                if rb_status != last_phase:
                    log_info(f"  Backup {rb_id} status={rb_status}")
                    last_phase = rb_status

        if not is_running:
            try:
                history = api_get(base_url, "/api/dashboard/history?page=1&size=5")
                records = history if isinstance(history, list) else history.get("items", history.get("data", []))
                if not isinstance(records, list):
                    records = []
                for rec in records:
                    rec_id = rec.get("id")
                    rec_status = rec.get("status")
                    if backup_id is not None and rec_id != backup_id:
                        continue
                    if rec_status == "completed":
                        log_info(f"Backup {rec_id} completed successfully (from history)")
                        return rec
                    elif rec_status == "failed":
                        err = rec.get("error_message", "unknown error")
                        raise RuntimeError(f"Backup {rec_id} failed: {err}")
            except Exception as e:
                log_warn(f"History poll failed: {e}")

            time.sleep(poll_interval)
            continue

        time.sleep(poll_interval)

    raise TimeoutError(f"Backup did not complete within {timeout_sec} seconds")


def main():
    global requests

    parser = argparse.ArgumentParser(
        description="NAS Backup closed-loop test: backup → OSS upload → restore → verify",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("--api", default="http://localhost:8080", help="Backend API base URL (default: http://localhost:8080)")
    parser.add_argument("--file", required=True, help="Absolute path to the file to test backup/restore for")
    parser.add_argument("--type", default="full", choices=["full", "incremental", "auto"], help="Backup type (default: full)")
    parser.add_argument("--output-dir", default=None, help="Directory to restore to (default: temporary directory)")
    parser.add_argument("--keep-artifacts", action="store_true", help="Keep restored files after test (don't clean up)")
    parser.add_argument("--backup-timeout", type=int, default=3600, help="Backup timeout in seconds (default: 3600)")
    parser.add_argument("--restore-timeout", type=int, default=3600, help="Restore timeout in seconds (default: 3600)")
    parser.add_argument("--skip-backup", action="store_true", help="Skip backup step, only restore an existing backup")
    parser.add_argument("--poll-interval", type=float, default=2.0, help="Status poll interval in seconds (default: 2.0)")
    args = parser.parse_args()

    try:
        import requests as _requests
        requests = _requests
    except ImportError:
        print("Error: 'requests' library is required. Install with: pip3 install requests")
        sys.exit(1)

    test_file = os.path.abspath(args.file)
    if not os.path.isfile(test_file):
        log_fail(f"Test file does not exist or is not a file: {test_file}")
        sys.exit(1)

    base_url = args.api.rstrip("/")
    restore_dir = args.output_dir
    cleanup_restore = False
    test_passed = False
    temp_dir = None

    results = {
        "api": base_url,
        "test_file": test_file,
        "backup_type": args.type,
        "original_hash": None,
        "original_size": None,
        "restored_path": None,
        "restored_hash": None,
        "restored_size": None,
        "backup_id": None,
        "backup_status": None,
        "restore_result": None,
        "hash_match": None,
        "errors": [],
    }

    try:
        log_step("STEP 1: Pre-flight checks")
        log_info(f"Test file: {test_file}")
        file_size = os.path.getsize(test_file)
        results["original_size"] = file_size
        log_info(f"File size: {file_size:,} bytes")

        try:
            health = api_get(base_url, "/api/storage/health")
            log_success(f"OSS storage healthy (latency: {health.get('latency_ms', '?')}ms)")
        except Exception as e:
            results["errors"].append(f"Storage health check failed: {e}")
            log_fail(f"OSS storage health check failed: {e}")
            log_warn("Continuing anyway, but backup/restore may fail...")

        log_step("STEP 2: Calculate original file hash")
        original_hash = sha256_file(test_file)
        results["original_hash"] = original_hash
        log_info(f"SHA256: {original_hash}")

        backup_record = None
        if not args.skip_backup:
            log_step("STEP 3: Trigger backup")
            log_info(f"Triggering {args.type} backup...")
            try:
                trigger_resp = api_post(base_url, "/api/backup/trigger", {"type": args.type})
                backup_id = trigger_resp.get("backup_id")
                results["backup_id"] = backup_id
                log_info(f"Backup triggered, backup_id={backup_id}")
            except Exception as e:
                results["errors"].append(f"Failed to trigger backup: {e}")
                log_fail(f"Failed to trigger backup: {e}")
                raise

            log_step("STEP 4: Wait for backup completion")
            try:
                backup_record = wait_for_backup_completion(
                    base_url,
                    backup_id,
                    timeout_sec=args.backup_timeout,
                    poll_interval=args.poll_interval,
                )
                results["backup_status"] = backup_record.get("status")
                log_success(f"Backup completed: {json.dumps({k: backup_record.get(k) for k in ['id', 'status', 'total_files', 'total_size', 'uploaded_size', 'skipped_by_dedup']}, indent=None)}")
            except Exception as e:
                results["errors"].append(f"Backup failed/timed out: {e}")
                log_fail(f"Backup did not complete successfully: {e}")
                raise
        else:
            log_step("STEP 3/4: Skipping backup (--skip-backup)")
            log_info("Checking latest completed backup...")
            try:
                history = api_get(base_url, "/api/dashboard/history?page=1&size=1")
                records = history if isinstance(history, list) else history.get("items", history.get("data", []))
                if isinstance(records, list) and len(records) > 0:
                    backup_record = records[0]
                    results["backup_id"] = backup_record.get("id")
                    results["backup_status"] = backup_record.get("status")
                    log_info(f"Using existing backup {backup_record.get('id')} (status={backup_record.get('status')})")
            except Exception as e:
                log_warn(f"Could not fetch backup history: {e}")

        log_step("STEP 5: Prepare restore directory")
        if restore_dir is None:
            temp_dir = tempfile.mkdtemp(prefix="nas-restore-test-")
            restore_dir = temp_dir
            cleanup_restore = True
            log_info(f"Created temp restore dir: {restore_dir}")
        else:
            restore_dir = os.path.abspath(restore_dir)
            os.makedirs(restore_dir, exist_ok=True)
            log_info(f"Using restore dir: {restore_dir}")

        log_step("STEP 6: Trigger restore")
        log_info(f"Restoring file: {test_file}")
        log_info(f"Restore to: {restore_dir}")
        restore_backup_id = results.get("backup_id")
        if restore_backup_id:
            log_info(f"Restoring from backup_id={restore_backup_id}")
        try:
            restore_req = {
                "paths": [test_file],
                "output_dir": restore_dir,
            }
            if restore_backup_id is not None:
                restore_req["backup_id"] = restore_backup_id
            restore_result = api_post(base_url, "/api/restore", restore_req, timeout=args.restore_timeout)
            results["restore_result"] = restore_result
            restored_count = restore_result.get("restored_files", 0)
            failed_files = restore_result.get("failed_files", [])
            total = restore_result.get("total_files", 0)
            elapsed = restore_result.get("elapsed_ms", 0)
            log_info(f"Restore finished in {elapsed}ms: total={total}, restored={restored_count}, failed={len(failed_files)}")
            if failed_files:
                for f in failed_files:
                    log_warn(f"  Failed: {f}")
                results["errors"].append(f"Some files failed to restore: {failed_files}")
            if restored_count < 1:
                raise RuntimeError(f"Restore reported 0 files restored (expected 1)")
        except Exception as e:
            results["errors"].append(f"Restore failed: {e}")
            log_fail(f"Restore failed: {e}")
            raise

        log_step("STEP 7: Locate restored file and verify hash")
        restored_path = None
        orig_parent = os.path.basename(os.path.dirname(test_file))
        orig_name = os.path.basename(test_file)
        candidates = [
            os.path.join(restore_dir, orig_name),
            os.path.join(restore_dir, orig_parent, orig_name),
        ]
        for root, dirs, files in os.walk(restore_dir):
            for f in files:
                if f == orig_name:
                    candidates.append(os.path.join(root, f))
        for c in candidates:
            if os.path.isfile(c):
                restored_path = c
                break
        if restored_path is None:
            log_fail(f"Could not find restored file in {restore_dir}!")
            log_info(f"Contents of restore dir:")
            for root, dirs, files in os.walk(restore_dir):
                for f in files:
                    log_info(f"  {os.path.join(root, f)}")
            results["errors"].append("Restored file not found in output directory")
            raise RuntimeError("Restored file not found")

        results["restored_path"] = restored_path
        restored_size = os.path.getsize(restored_path)
        results["restored_size"] = restored_size
        log_info(f"Restored file: {restored_path}")
        log_info(f"Restored size: {restored_size:,} bytes")

        restored_hash = sha256_file(restored_path)
        results["restored_hash"] = restored_hash
        log_info(f"Restored SHA256: {restored_hash}")

        log_step("STEP 8: Final verification")
        size_match = (file_size == restored_size)
        hash_match = (original_hash == restored_hash)
        results["hash_match"] = hash_match

        if size_match:
            log_success(f"Size match: {file_size:,} bytes")
        else:
            log_fail(f"Size MISMATCH: original={file_size}, restored={restored_size}")

        if hash_match:
            log_success(f"Hash match: {original_hash}")
            log_success("DATA INTEGRITY VERIFIED - Backup/Restore closed-loop PASSED")
            test_passed = True
        else:
            log_fail(f"Hash MISMATCH!")
            log_fail(f"  Original: {original_hash}")
            log_fail(f"  Restored: {restored_hash}")
            log_fail("DATA INTEGRITY CHECK FAILED")

    except Exception as e:
        log_fail(f"Test aborted due to error: {e}")
        results["errors"].append(str(e))
    finally:
        log_step("TEST SUMMARY")
        print(json.dumps(results, indent=2, default=str))

        if temp_dir and cleanup_restore and not args.keep_artifacts:
            log_info(f"Cleaning up temp directory: {temp_dir}")
            try:
                shutil.rmtree(temp_dir)
            except Exception as e:
                log_warn(f"Cleanup failed: {e}")

        if test_passed:
            print(f"\n{Colors.GREEN}{Colors.BOLD}*** CLOSED-LOOP TEST PASSED ***{Colors.RESET}")
            sys.exit(0)
        else:
            print(f"\n{Colors.RED}{Colors.BOLD}*** CLOSED-LOOP TEST FAILED ***{Colors.RESET}")
            sys.exit(1)


if __name__ == "__main__":
    main()
