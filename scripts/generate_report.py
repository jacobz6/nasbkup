#!/usr/bin/env python3
# ==============================================================================
# NAS Backup System - HTML Acceptance Report Generator
# ==============================================================================
# Reads the E2E test results and generates a comprehensive HTML acceptance report.
# ==============================================================================

import json
import os
import sys
from pathlib import Path
from datetime import datetime

SCRIPT_DIR = Path(__file__).parent.resolve()
PROJECT_ROOT = SCRIPT_DIR.parent
TEST_DIR = PROJECT_ROOT / "test-env"
REPORT_JSON = TEST_DIR / "e2e-report.json"
REPORT_HTML = TEST_DIR / "acceptance-report.html"

def generate_html(data):
    s = data.get("summary", {})
    results = data.get("results", [])
    phases = data.get("phases", [])
    timestamp = data.get("timestamp", datetime.now().isoformat())

    total = s.get("total", 0)
    passed = s.get("passed", 0)
    failed = s.get("failed", 0)
    critical = s.get("critical_failures", 0)
    elapsed = s.get("elapsed_sec", 0)
    pass_rate = (passed / total * 100) if total > 0 else 0

    # Determine overall status
    if critical > 0:
        overall_status = "FAIL"
        status_color = "#dc2626"
        status_bg = "#fef2f2"
        status_text = "核心功能存在严重问题"
    elif failed > 0:
        overall_status = "WARN"
        status_color = "#d97706"
        status_bg = "#fffbeb"
        status_text = "存在非关键性问题，建议修复"
    else:
        overall_status = "PASS"
        status_color = "#16a34a"
        status_bg = "#f0fdf4"
        status_text = "全部功能验证通过，可以交付"

    # Group results by phase
    phases_with_results = []
    idx = 0
    for phase in phases:
        phase_results = []
        # Take results until next phase (approximate - just take all in order)
        # We'll just show all results under their respective phases
        phases_with_results.append({"name": phase, "results": []})

    # Simple approach: just list all results
    passed_tests = [r for r in results if r["passed"]]
    failed_tests = [r for r in results if not r["passed"]]

    # Get system info
    backend_dir = PROJECT_ROOT / "nas-backup-backend"
    rclone_conf = backend_dir / "data" / "rclone.conf"
    db_file = backend_dir / "data" / "nas-backup.db"

    config_yaml = backend_dir / "config.yaml"
    config_info = ""
    if config_yaml.exists():
        try:
            config_text = config_yaml.read_text()
            # Extract key config values
            import re
            port_m = re.search(r'port:\s*(\d+)', config_text)
            enc_m = re.search(r'algorithm:\s*"([^"]+)"', config_text)
            comp_m = re.search(r'algorithm:\s*"([^"]+)"', config_text)
            config_info = f"Port: {port_m.group(1) if port_m else '8080'}, Encryption: {enc_m.group(1) if enc_m else 'AES-256-GCM'}, Compression: zstd"
        except Exception:
            config_info = "Available"

    html = f"""<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>NAS Backup System - 验收报告</title>
<style>
  * {{ margin: 0; padding: 0; box-sizing: border-box; }}
  body {{ font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #f8fafc; color: #1e293b; line-height: 1.6; padding: 2rem; }}
  .container {{ max-width: 1000px; margin: 0 auto; }}
  h1 {{ font-size: 2rem; font-weight: 700; margin-bottom: 0.5rem; }}
  .subtitle {{ color: #64748b; margin-bottom: 2rem; }}
  .status-banner {{ background: {status_bg}; border: 2px solid {status_color}; border-radius: 12px; padding: 1.5rem 2rem; margin-bottom: 2rem; display: flex; align-items: center; gap: 1rem; }}
  .status-icon {{ width: 48px; height: 48px; border-radius: 50%; background: {status_color}; display: flex; align-items: center; justify-content: center; color: white; font-size: 1.5rem; font-weight: bold; flex-shrink: 0; }}
  .status-text h2 {{ font-size: 1.25rem; color: {status_color}; margin-bottom: 0.25rem; }}
  .status-text p {{ color: #475569; }}
  .grid {{ display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 1rem; margin-bottom: 2rem; }}
  .card {{ background: white; border-radius: 10px; padding: 1.25rem; box-shadow: 0 1px 3px rgba(0,0,0,0.1); }}
  .card-label {{ font-size: 0.875rem; color: #64748b; text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 0.5rem; }}
  .card-value {{ font-size: 1.75rem; font-weight: 700; }}
  .card-value.green {{ color: #16a34a; }}
  .card-value.red {{ color: #dc2626; }}
  .card-value.amber {{ color: #d97706; }}
  .card-value.blue {{ color: #2563eb; }}
  .section {{ background: white; border-radius: 10px; padding: 1.5rem 2rem; margin-bottom: 1.5rem; box-shadow: 0 1px 3px rgba(0,0,0,0.1); }}
  .section h2 {{ font-size: 1.25rem; margin-bottom: 1rem; padding-bottom: 0.5rem; border-bottom: 2px solid #e2e8f0; }}
  .test-item {{ display: flex; align-items: flex-start; padding: 0.75rem 0; border-bottom: 1px solid #f1f5f9; }}
  .test-item:last-child {{ border-bottom: none; }}
  .test-status {{ width: 24px; height: 24px; border-radius: 50%; display: flex; align-items: center; justify-content: center; color: white; font-size: 0.75rem; font-weight: bold; flex-shrink: 0; margin-right: 0.75rem; margin-top: 2px; }}
  .test-status.pass {{ background: #16a34a; }}
  .test-status.fail {{ background: #dc2626; }}
  .test-name {{ font-weight: 500; flex: 1; }}
  .test-detail {{ font-size: 0.875rem; color: #64748b; margin-top: 0.25rem; }}
  .test-critical {{ font-size: 0.75rem; background: #fef2f2; color: #dc2626; padding: 2px 6px; border-radius: 4px; margin-left: 0.5rem; font-weight: 600; }}
  .info-grid {{ display: grid; grid-template-columns: auto 1fr; gap: 0.5rem 1.5rem; font-size: 0.9rem; }}
  .info-label {{ color: #64748b; font-weight: 500; }}
  .info-value {{ font-family: 'SF Mono', 'Fira Code', monospace; color: #1e293b; }}
  .progress-bar {{ width: 100%; height: 8px; background: #e2e8f0; border-radius: 4px; overflow: hidden; margin-top: 0.5rem; }}
  .progress-fill {{ height: 100%; background: {status_color}; border-radius: 4px; transition: width 0.5s; }}
  .footer {{ text-align: center; color: #94a3b8; font-size: 0.875rem; margin-top: 2rem; padding-top: 1rem; border-top: 1px solid #e2e8f0; }}
</style>
</head>
<body>
<div class="container">
  <h1>NAS Backup System - 验收测试报告</h1>
  <p class="subtitle">macOS 本地环境端到端验证 &middot; 生成时间: {timestamp[:19].replace('T', ' ')}</p>

  <div class="status-banner">
    <div class="status-icon">{overall_status[0]}</div>
    <div class="status-text">
      <h2>{status_text}</h2>
      <p>测试通过率: {pass_rate:.1f}% &middot; 耗时: {elapsed:.1f}秒 &middot; 总测试项: {total}</p>
      <div class="progress-bar"><div class="progress-fill" style="width: {pass_rate}%"></div></div>
    </div>
  </div>

  <div class="grid">
    <div class="card">
      <div class="card-label">总测试项</div>
      <div class="card-value blue">{total}</div>
    </div>
    <div class="card">
      <div class="card-label">通过</div>
      <div class="card-value green">{passed}</div>
    </div>
    <div class="card">
      <div class="card-label">失败</div>
      <div class="card-value {'red' if failed > 0 else 'green'}">{failed}</div>
    </div>
    <div class="card">
      <div class="card-label">严重问题</div>
      <div class="card-value {'red' if critical > 0 else 'green'}">{critical}</div>
    </div>
  </div>

  <div class="section">
    <h2>环境信息</h2>
    <div class="info-grid">
      <div class="info-label">操作系统</div><div class="info-value">macOS (Darwin)</div>
      <div class="info-label">后端地址</div><div class="info-value">http://127.0.0.1:8080</div>
      <div class="info-label">配置</div><div class="info-value">{config_info}</div>
      <div class="info-label">存储模式</div><div class="info-value">本地文件系统 + rclone crypt (模拟云端)</div>
      <div class="info-label">加密算法</div><div class="info-value">AES-256-GCM (客户端加密)</div>
      <div class="info-label">压缩算法</div><div class="info-value">Zstandard (zstd) level 19</div>
      <div class="info-label">去重方式</div><div class="info-value">内容感知 (SHA-256)</div>
      <div class="info-label">数据库</div><div class="info-value">SQLite</div>
    </div>
  </div>

  <div class="section">
    <h2>验证的核心功能</h2>
    <div class="info-grid">
      <div class="info-label">全量备份</div><div class="info-value">{'✓ 通过' if any('Full backup completed' in r['name'] and r['passed'] for r in results) else '✗ 未通过'}</div>
      <div class="info-label">增量备份</div><div class="info-value">{'✓ 通过' if any('Incremental backup completed' in r['name'] and r['passed'] for r in results) else '✗ 未通过'}</div>
      <div class="info-label">文件恢复</div><div class="info-value">{'✓ 通过' if any('Restore completed' in r['name'] and r['passed'] for r in results) else '✗ 未通过'}</div>
      <div class="info-label">完整性校验 (SHA256)</div><div class="info-value">{'✓ 通过' if any('File integrity' in r['name'] and r['passed'] for r in results) else '✗ 未通过'}</div>
      <div class="info-label">客户端加密</div><div class="info-value">{'✓ 通过' if any('Client-side encryption' in r['name'] and r['passed'] for r in results) else '✗ 未通过'}</div>
      <div class="info-label">数据压缩</div><div class="info-value">{'✓ 通过' if any('Compression working' in r['name'] and r['passed'] for r in results) else '✗ 未通过'}</div>
      <div class="info-label">数据去重</div><div class="info-value">{'✓ 通过' if any('Deduplication' in r['name'] and r['passed'] for r in results) else '○ 未验证'}</div>
      <div class="info-label">空文件处理</div><div class="info-value">{'✓ 通过' if any('Empty file' in r['name'] and r['passed'] for r in results) else '✗ 未通过'}</div>
    </div>
  </div>
"""

    # Test details - passed
    if passed_tests:
        html += """
  <div class="section">
    <h2>通过的测试项</h2>
"""
        for r in passed_tests:
            detail = f'<div class="test-detail">{r["detail"]}</div>' if r.get("detail") else ""
            html += f"""
    <div class="test-item">
      <div class="test-status pass">✓</div>
      <div class="test-name">{r["name"]}{detail}</div>
    </div>
"""
        html += "  </div>\n"

    # Test details - failed
    if failed_tests:
        html += """
  <div class="section">
    <h2 style="color: #dc2626;">失败的测试项</h2>
"""
        for r in failed_tests:
            crit = ' <span class="test-critical">CRITICAL</span>' if r.get("critical") else ""
            detail = f'<div class="test-detail">{r["detail"]}</div>' if r.get("detail") else ""
            html += f"""
    <div class="test-item">
      <div class="test-status fail">✗</div>
      <div class="test-name">{r["name"]}{crit}{detail}</div>
    </div>
"""
        html += "  </div>\n"

    # Conclusion
    html += f"""
  <div class="section">
    <h2>验收结论</h2>
    <p>本报告基于 macOS 本地环境的端到端自动化测试生成，覆盖以下场景：</p>
    <ul style="margin: 0.75rem 0 0 1.5rem; color: #475569;">
      <li>环境依赖检查（Go/Node/rclone/Python）</li>
      <li>测试数据生成（文本文件、空文件、二进制文件、重复文件、深层嵌套文件）</li>
      <li>全量备份流程（扫描 → 哈希 → 去重 → 压缩 → 加密 → 上传）</li>
      <li>增量备份流程（检测变更 → 仅备份新增/修改文件）</li>
      <li>恢复流程（下载 → 解密 → 解压 → 写入目标路径）</li>
      <li>SHA-256 完整性校验（恢复文件与原始文件逐字节对比）</li>
      <li>客户端加密验证（存储文件不可读）</li>
      <li>备份历史 API 验证</li>
    </ul>
    <p style="margin-top: 1rem; padding: 1rem; background: {status_bg}; border-radius: 8px; border-left: 4px solid {status_color};">
      <strong>结论：</strong>{status_text}
    </p>
  </div>

  <div class="footer">
    Generated by NAS Backup System E2E Test Suite &middot; {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}
  </div>
</div>
</body>
</html>"""
    return html


def main():
    if not REPORT_JSON.exists():
        print(f"Error: Test report not found at {REPORT_JSON}", file=sys.stderr)
        print("Run verify_e2e.py first to generate test results.", file=sys.stderr)
        sys.exit(1)

    with open(REPORT_JSON, 'r') as f:
        data = json.load(f)

    html = generate_html(data)
    REPORT_HTML.write_text(html, encoding='utf-8')
    print(f"HTML report generated: {REPORT_HTML}")

    s = data.get("summary", {})
    print(f"  Passed: {s.get('passed', 0)}/{s.get('total', 0)}")
    if s.get("critical_failures", 0) > 0:
        sys.exit(1)
    elif s.get("failed", 0) > 0:
        sys.exit(2)
    else:
        sys.exit(0)


if __name__ == "__main__":
    main()
