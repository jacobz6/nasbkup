#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
NAS 文件系统模拟器
用于生成测试文件，模拟 NAS 中的各种文件类型和大小分布。

用法示例:
    python nas_file_generator.py --count 100 --total-size 1G --output ./test_nas
    python nas_file_generator.py --count 500 --total-size 5G --output ./test_nas --depth 3
"""

import os
import sys
import random
import string
import argparse
import struct
from pathlib import Path
from typing import List, Tuple, Optional


# ============ 文件类型配置 ============
FILE_TYPES = {
    # 文档类
    "txt": {"ext": ".txt", "category": "document", "min_size": 1, "max_size": 10 * 1024 * 1024},
    "pdf": {"ext": ".pdf", "category": "document", "min_size": 1024, "max_size": 50 * 1024 * 1024},
    "docx": {"ext": ".docx", "category": "document", "min_size": 10 * 1024, "max_size": 20 * 1024 * 1024},
    "xlsx": {"ext": ".xlsx", "category": "document", "min_size": 5 * 1024, "max_size": 10 * 1024 * 1024},
    "pptx": {"ext": ".pptx", "category": "document", "min_size": 20 * 1024, "max_size": 100 * 1024 * 1024},
    "md": {"ext": ".md", "category": "document", "min_size": 100, "max_size": 5 * 1024 * 1024},

    # 图片类
    "jpg": {"ext": ".jpg", "category": "image", "min_size": 10 * 1024, "max_size": 20 * 1024 * 1024},
    "png": {"ext": ".png", "category": "image", "min_size": 5 * 1024, "max_size": 50 * 1024 * 1024},
    "gif": {"ext": ".gif", "category": "image", "min_size": 1 * 1024, "max_size": 10 * 1024 * 1024},
    "bmp": {"ext": ".bmp", "category": "image", "min_size": 100 * 1024, "max_size": 100 * 1024 * 1024},

    # 视频类
    "mp4": {"ext": ".mp4", "category": "video", "min_size": 1 * 1024 * 1024, "max_size": 5 * 1024 * 1024 * 1024},
    "avi": {"ext": ".avi", "category": "video", "min_size": 5 * 1024 * 1024, "max_size": 10 * 1024 * 1024 * 1024},
    "mkv": {"ext": ".mkv", "category": "video", "min_size": 10 * 1024 * 1024, "max_size": 20 * 1024 * 1024 * 1024},
    "mov": {"ext": ".mov", "category": "video", "min_size": 2 * 1024 * 1024, "max_size": 5 * 1024 * 1024 * 1024},

    # 音频类
    "mp3": {"ext": ".mp3", "category": "audio", "min_size": 100 * 1024, "max_size": 50 * 1024 * 1024},
    "wav": {"ext": ".wav", "category": "audio", "min_size": 500 * 1024, "max_size": 500 * 1024 * 1024},
    "flac": {"ext": ".flac", "category": "audio", "min_size": 5 * 1024 * 1024, "max_size": 200 * 1024 * 1024},

    # 代码类
    "py": {"ext": ".py", "category": "code", "min_size": 100, "max_size": 2 * 1024 * 1024},
    "js": {"ext": ".js", "category": "code", "min_size": 100, "max_size": 5 * 1024 * 1024},
    "html": {"ext": ".html", "category": "code", "min_size": 1 * 1024, "max_size": 1 * 1024 * 1024},
    "css": {"ext": ".css", "category": "code", "min_size": 500, "max_size": 500 * 1024},
    "java": {"ext": ".java", "category": "code", "min_size": 500, "max_size": 1 * 1024 * 1024},
    "cpp": {"ext": ".cpp", "category": "code", "min_size": 500, "max_size": 2 * 1024 * 1024},
    "go": {"ext": ".go", "category": "code", "min_size": 500, "max_size": 1 * 1024 * 1024},
    "sql": {"ext": ".sql", "category": "code", "min_size": 1 * 1024, "max_size": 10 * 1024 * 1024},

    # 压缩/归档类
    "zip": {"ext": ".zip", "category": "archive", "min_size": 1 * 1024, "max_size": 5 * 1024 * 1024 * 1024},
    "rar": {"ext": ".rar", "category": "archive", "min_size": 1 * 1024, "max_size": 5 * 1024 * 1024 * 1024},
    "tar": {"ext": ".tar", "category": "archive", "min_size": 1 * 1024, "max_size": 10 * 1024 * 1024 * 1024},
    "gz": {"ext": ".gz", "category": "archive", "min_size": 1 * 1024, "max_size": 2 * 1024 * 1024 * 1024},

    # 其他
    "exe": {"ext": ".exe", "category": "other", "min_size": 10 * 1024, "max_size": 500 * 1024 * 1024},
    "dll": {"ext": ".dll", "category": "other", "min_size": 10 * 1024, "max_size": 100 * 1024 * 1024},
    "iso": {"ext": ".iso", "category": "other", "min_size": 100 * 1024 * 1024, "max_size": 10 * 1024 * 1024 * 1024},
    "db": {"ext": ".db", "category": "other", "min_size": 1 * 1024, "max_size": 5 * 1024 * 1024 * 1024},
    "log": {"ext": ".log", "category": "other", "min_size": 100, "max_size": 1 * 1024 * 1024 * 1024},
}

# 各类别的默认权重（影响生成概率）
CATEGORY_WEIGHTS = {
    "document": 20,
    "image": 15,
    "video": 5,
    "audio": 5,
    "code": 20,
    "archive": 5,
    "other": 10,
}

# 模拟的 NAS 目录结构
NAS_DIRECTORIES = [
    "Documents/Work",
    "Documents/Personal",
    "Documents/Projects",
    "Pictures/Family",
    "Pictures/Travel",
    "Pictures/Screenshots",
    "Videos/Movies",
    "Videos/TVShows",
    "Videos/HomeVideos",
    "Music/Rock",
    "Music/Pop",
    "Music/Classical",
    "Software/Windows",
    "Software/Linux",
    "Software/macOS",
    "Backups/Daily",
    "Backups/Weekly",
    "Backups/Monthly",
    "Projects/Web",
    "Projects/Mobile",
    "Projects/AI",
    "Shared/Public",
    "Shared/Team",
    "Logs/System",
    "Logs/Application",
    "Temp",
    "Downloads",
    "Uploads",
]


def parse_size(size_str: str) -> int:
    """解析大小字符串，如 '1G', '500M', '10K'"""
    size_str = size_str.strip().upper()
    multipliers = {
        'B': 1,
        'K': 1024,
        'M': 1024 ** 2,
        'G': 1024 ** 3,
        'T': 1024 ** 4,
    }
    if size_str[-1] in multipliers:
        return int(float(size_str[:-1]) * multipliers[size_str[-1]])
    return int(size_str)


def format_size(size_bytes: int) -> str:
    """格式化字节大小为人类可读字符串"""
    for unit in ['B', 'KB', 'MB', 'GB', 'TB']:
        if size_bytes < 1024.0:
            return f"{size_bytes:.2f} {unit}"
        size_bytes /= 1024.0
    return f"{size_bytes:.2f} PB"


def generate_random_name(length: int = 8) -> str:
    """生成随机文件名（不含扩展名）"""
    return ''.join(random.choices(string.ascii_letters + string.digits + '_-', k=length))


def generate_random_text(size: int) -> bytes:
    """生成随机文本内容"""
    # 生成一些看起来像真实文本的内容
    words = [
        "Lorem", "ipsum", "dolor", "sit", "amet", "consectetur", "adipiscing", "elit",
        "NAS", "storage", "file", "system", "data", "backup", "server", "network",
        "python", "java", "javascript", "code", "program", "function", "class", "method",
        "image", "video", "audio", "document", "archive", "compressed", "encrypted",
        "user", "admin", "root", "password", "login", "session", "token", "cookie",
        "error", "warning", "info", "debug", "trace", "log", "record", "history",
        "config", "settings", "options", "preferences", "profile", "account", "database",
        "query", "select", "insert", "update", "delete", "create", "drop", "table",
        "index", "view", "trigger", "procedure", "function", "variable", "constant",
        "import", "export", "module", "package", "library", "framework", "toolkit",
        "测试", "文件", "数据", "系统", "网络", "服务器", "存储", "备份", "日志",
    ]
    content = []
    current_size = 0
    while current_size < size:
        word = random.choice(words)
        line = word + random.choice([' ', '\n', '\t', '. ', ', '])
        encoded = line.encode('utf-8')
        if current_size + len(encoded) > size:
            break
        content.append(encoded)
        current_size += len(encoded)
    # 补齐剩余大小
    remaining = size - current_size
    if remaining > 0:
        content.append(b' ' * remaining)
    return b''.join(content)


def generate_file_with_size(filepath: Path, size: int, file_type: str) -> None:
    """生成指定大小和类型的文件"""
    filepath.parent.mkdir(parents=True, exist_ok=True)

    # 根据文件类型生成不同内容
    if file_type in ["jpg", "png", "gif", "bmp", "mp4", "avi", "mkv", "mov",
                      "mp3", "wav", "flac", "exe", "dll", "iso", "zip", "rar", "tar", "gz"]:
        # 二进制文件：写入随机字节
        with open(filepath, 'wb') as f:
            # 使用更高效的写入方式
            chunk_size = 1024 * 1024  # 1MB 块
            written = 0
            while written < size:
                to_write = min(chunk_size, size - written)
                f.write(os.urandom(to_write))
                written += to_write
    else:
        # 文本文件：写入随机文本
        with open(filepath, 'wb') as f:
            chunk_size = 1024 * 1024
            written = 0
            while written < size:
                to_write = min(chunk_size, size - written)
                f.write(generate_random_text(to_write))
                written += to_write


def select_file_type() -> str:
    """根据类别权重随机选择文件类型"""
    # 先按权重选择类别
    categories = list(CATEGORY_WEIGHTS.keys())
    weights = list(CATEGORY_WEIGHTS.values())
    selected_category = random.choices(categories, weights=weights, k=1)[0]

    # 再从该类别中随机选择一个文件类型
    type_candidates = [k for k, v in FILE_TYPES.items() if v["category"] == selected_category]
    return random.choice(type_candidates)


def generate_random_size(file_type: str, min_size: Optional[int] = None,
                          max_size: Optional[int] = None) -> int:
    """为指定文件类型生成随机大小"""
    type_info = FILE_TYPES[file_type]
    lo = min_size if min_size is not None else type_info["min_size"]
    hi = max_size if max_size is not None else type_info["max_size"]

    # 使用对数分布，让小文件更常见
    log_lo = max(0, lo)
    log_hi = max(log_lo + 1, hi)

    # 80% 概率使用类型自身的范围，20% 概率突破到全局范围
    if random.random() < 0.8:
        log_lo = max(0, type_info["min_size"])
        log_hi = max(log_lo + 1, type_info["max_size"])

    # 在对数空间生成随机数
    import math
    log_min = math.log10(log_lo + 1)
    log_max = math.log10(log_hi + 1)
    random_log = random.uniform(log_min, log_max)
    size = int(10 ** random_log) - 1

    return max(lo, min(hi, size))


def create_directory_structure(base_path: Path, depth: int) -> List[Path]:
    """创建模拟的 NAS 目录结构"""
    directories = [base_path]

    for dir_path in NAS_DIRECTORIES:
        parts = dir_path.split('/')
        # 根据深度限制目录层级
        parts = parts[:depth]
        full_path = base_path.joinpath(*parts)
        full_path.mkdir(parents=True, exist_ok=True)
        if full_path not in directories:
            directories.append(full_path)

    # 再随机生成一些深层嵌套目录
    num_extra_dirs = random.randint(depth * 2, depth * 5)
    for _ in range(num_extra_dirs):
        parent = random.choice(directories)
        new_dir = parent / generate_random_name(random.randint(3, 12))
        new_dir.mkdir(parents=True, exist_ok=True)
        if new_dir not in directories:
            directories.append(new_dir)

    return directories


def generate_files_by_count(args) -> None:
    """按文件数量生成文件，若同时指定了总大小则作为上限"""
    base_path = Path(args.output).resolve()
    base_path.mkdir(parents=True, exist_ok=True)

    directories = create_directory_structure(base_path, args.depth)

    total_size = 0
    file_counts = {cat: 0 for cat in set(v["category"] for v in FILE_TYPES.values())}

    # 若同时指定了 total_size，则作为总大小上限
    size_limit = parse_size(args.total_size) if args.total_size else None

    print(f"开始生成 {args.count} 个文件到: {base_path}")
    print(f"目录深度: {args.depth}")
    if size_limit:
        print(f"总大小上限: {format_size(size_limit)}")
    print("-" * 50)

    for i in range(args.count):
        file_type = select_file_type()

        # 计算剩余可用大小
        remaining = size_limit - total_size if size_limit else None
        if remaining is not None and remaining <= 0:
            print(f"  已达到总大小上限，提前停止。已生成 {i} 个文件")
            break

        # 限制单个文件大小不超过剩余空间
        max_size = args.max_file_size
        if remaining is not None:
            max_size = min(args.max_file_size, remaining)

        size = generate_random_size(file_type, args.min_file_size, max_size)
        if remaining is not None:
            size = min(size, remaining)

        # 选择目录
        directory = random.choice(directories)
        filename = f"{generate_random_name(random.randint(5, 20))}{FILE_TYPES[file_type]['ext']}"
        filepath = directory / filename

        # 处理文件名冲突
        counter = 1
        while filepath.exists():
            stem = filepath.stem
            if '_' in stem and stem.rsplit('_', 1)[1].isdigit():
                stem = stem.rsplit('_', 1)[0]
            filepath = directory / f"{stem}_{counter}{FILE_TYPES[file_type]['ext']}"
            counter += 1

        generate_file_with_size(filepath, size, file_type)

        total_size += size
        file_counts[FILE_TYPES[file_type]["category"]] += 1

        if (i + 1) % max(1, args.count // 20) == 0 or i == args.count - 1:
            progress = (i + 1) / args.count * 100
            print(f"  进度: {progress:.1f}% | 已生成 {i + 1}/{args.count} 个文件 | "
                  f"总大小: {format_size(total_size)}")

    actual_count = sum(file_counts.values())
    print("-" * 50)
    print("生成完成!")
    print(f"总文件数: {actual_count}")
    print(f"总大小: {format_size(total_size)}")
    print("\n各类别文件分布:")
    for cat, count in sorted(file_counts.items()):
        if count > 0:
            print(f"  {cat:12s}: {count:4d} 个")


def generate_files_by_size(args) -> None:
    """按总大小生成文件"""
    base_path = Path(args.output).resolve()
    base_path.mkdir(parents=True, exist_ok=True)

    directories = create_directory_structure(base_path, args.depth)

    target_size = parse_size(args.total_size)
    current_size = 0
    file_count = 0
    file_counts = {cat: 0 for cat in set(v["category"] for v in FILE_TYPES.values())}

    print(f"开始生成文件，目标总大小: {format_size(target_size)}")
    print(f"输出目录: {base_path}")
    print(f"目录深度: {args.depth}")
    print("-" * 50)

    # 设置单个文件大小限制
    max_single_file = min(args.max_file_size, target_size // 10) if target_size >= 10 * 1024 * 1024 else args.max_file_size

    while current_size < target_size:
        file_type = select_file_type()
        remaining = target_size - current_size
        size = generate_random_size(file_type, args.min_file_size, min(max_single_file, remaining))
        size = min(size, remaining)  # 确保不超过目标

        # 选择目录
        directory = random.choice(directories)
        filename = f"{generate_random_name(random.randint(5, 20))}{FILE_TYPES[file_type]['ext']}"
        filepath = directory / filename

        # 处理文件名冲突
        counter = 1
        while filepath.exists():
            stem = filepath.stem
            if '_' in stem and stem.rsplit('_', 1)[1].isdigit():
                stem = stem.rsplit('_', 1)[0]
            filepath = directory / f"{stem}_{counter}{FILE_TYPES[file_type]['ext']}"
            counter += 1

        generate_file_with_size(filepath, size, file_type)

        current_size += size
        file_count += 1
        file_counts[FILE_TYPES[file_type]["category"]] += 1

        # 每生成一定数量或大小更新进度
        if file_count % 100 == 0 or current_size >= target_size:
            progress = current_size / target_size * 100
            print(f"  进度: {progress:.1f}% | 已生成 {file_count} 个文件 | "
                  f"当前大小: {format_size(current_size)}")

    print("-" * 50)
    print("生成完成!")
    print(f"总文件数: {file_count}")
    print(f"总大小: {format_size(current_size)} (目标: {format_size(target_size)})")
    print("\n各类别文件分布:")
    for cat, count in sorted(file_counts.items()):
        if count > 0:
            print(f"  {cat:12s}: {count:4d} 个")


def main():
    parser = argparse.ArgumentParser(
        description="NAS 文件系统模拟器 - 生成测试文件",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
示例:
  # 生成 100 个文件，总大小约 1GB
  python nas_file_generator.py --count 100 --total-size 1G --output ./test_nas

  # 仅按数量生成 500 个文件（不限制总大小）
  python nas_file_generator.py --count 500 --output ./test_nas

  # 仅按大小生成，目标 5GB
  python nas_file_generator.py --total-size 5G --output ./test_nas

  # 自定义目录深度和文件大小范围
  python nas_file_generator.py --count 1000 --total-size 2G --output ./test_nas \\
      --depth 4 --min-file-size 1K --max-file-size 100M
        """
    )

    parser.add_argument("--output", "-o", type=str, default="./test_nas",
                        help="输出目录路径 (默认: ./test_nas)")
    parser.add_argument("--count", "-n", type=int, default=None,
                        help="生成的文件数量")
    parser.add_argument("--total-size", "-s", type=str, default=None,
                        help="生成的文件总大小，支持 B/K/M/G/T 单位 (如: 1G, 500M)")
    parser.add_argument("--depth", "-d", type=int, default=3,
                        help="目录结构深度 (默认: 3)")
    parser.add_argument("--min-file-size", type=str, default="1B",
                        help="单个文件最小大小 (默认: 1B)")
    parser.add_argument("--max-file-size", type=str, default="10G",
                        help="单个文件最大大小 (默认: 10G)")
    parser.add_argument("--seed", type=int, default=None,
                        help="随机种子，用于复现结果")

    args = parser.parse_args()

    # 参数校验
    if args.count is None and args.total_size is None:
        parser.error("必须指定 --count 或 --total-size 至少一个参数")

    if args.count is not None and args.count <= 0:
        parser.error("--count 必须大于 0")

    # 解析大小参数
    args.min_file_size = parse_size(args.min_file_size)
    args.max_file_size = parse_size(args.max_file_size)

    if args.min_file_size > args.max_file_size:
        parser.error("--min-file-size 不能大于 --max-file-size")

    # 设置随机种子
    if args.seed is not None:
        random.seed(args.seed)
        print(f"使用随机种子: {args.seed}")

    # 执行生成
    if args.count is not None and args.total_size is not None:
        # 同时指定了数量和总大小：先生成数量，但限制总大小
        print("同时指定了 --count 和 --total-size，将以总大小为限制生成指定数量的文件")
        generate_files_by_count(args)
    elif args.count is not None:
        generate_files_by_count(args)
    else:
        generate_files_by_size(args)


if __name__ == "__main__":
    main()
