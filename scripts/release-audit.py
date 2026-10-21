#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""发布前自检：把「这个仓库能不能公开」变成一条可重复执行的命令。

人工查过一次不算数——下次改动加进一个 *.json 就悄悄带出去了。本脚本把发布边界
固化成可重复运行的断言（记录教训必须同时给机械闸门）。

    python scripts/release-audit.py          # 人类可读
    python scripts/release-audit.py --json   # 机器可读

退出码 0 = 全过；1 = 有 FAIL。
"""
import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SUFFIXES = (".jsonl", ".db", ".sqlite", ".sqlite3", ".csv", ".tsv", ".parquet")
NAME_HINTS = ("wechat_messages", "eval_set", "data.db", "messages.json")
LOCAL_DIRS = ("workmem", "WORKMEMORY", "docs/private", "node_modules", "dist")
ABS_PATTERNS = [re.compile(p, re.I) for p in (
    r"[A-Za-z]:\\Vault", r"/Users/[A-Za-z0-9_.-]+/")]
# C:\Users\<name>\ 只在 <name> 不是占位名时才算泄漏——测试夹具里的
# `C:\Users\someone\...` 是**虚构路径**，不是本机身份。
ABS_USER_RE = re.compile(r"C:\\Users\\([^\\\"'`\s]+)", re.I)
PLACEHOLDER_USERS = {"someone", "user", "username", "example", "test", "default",
                     "yourname", "your-user"}
SECRET_RE = re.compile(
    r"(api[_-]?key|secret|password|passwd|token)\s*[:=]\s*['\"][A-Za-z0-9_\-]{16,}['\"]", re.I)
# 测试哨兵（SENTINEL_… / EXAMPLE_… / DUMMY…）是断言用的假值，不是密钥。
SENTINEL_RE = re.compile(r"SENTINEL|EXAMPLE|DUMMY|FAKE|PLACEHOLDER|REDACTED", re.I)
FORBIDDEN = ("（方法细节见本地私有文档）", "已验证", "主密钥", "密钥的内存策略变更")


def git(*a):
    proc = subprocess.run(["git", "-C", str(ROOT)] + list(a), capture_output=True,
                          text=True, encoding="utf-8", errors="replace", check=False)
    return proc.stdout or ""


def blob(path):
    """读**工作树**内容（待发布的内容），文件不在磁盘上时回退到 HEAD。"""
    disk = ROOT / path
    if disk.is_file():
        try:
            return disk.read_text(encoding="utf-8", errors="replace")
        except OSError:
            pass
    p = subprocess.run(["git", "-C", str(ROOT), "show", "HEAD:" + path],
                       capture_output=True, text=True, encoding="utf-8",
                       errors="replace", check=False)
    return "" if p.returncode else (p.stdout or "")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()
    out = []

    def check(name, ok, detail):
        out.append({"check": name, "ok": bool(ok), "detail": detail})

    files = [f for f in git("ls-files").splitlines() if f.strip()]
    # 检查器自身必然含有它要找的字符串与模式（否则没法检查）——它不是"内容"。
    SELF = "scripts/release-audit.py"
    content_files = [f for f in files if f != SELF]

    bad = [f for f in files if f.lower().endswith(SUFFIXES)
           or any(h in Path(f).name.lower() for h in NAME_HINTS)]
    check("no-data-files", not bad, "数据形态文件 %d 个 %s" % (len(bad), bad[:5]))

    hist = set(l.strip() for l in git("log", "--all", "--diff-filter=A", "--name-only",
                                      "--pretty=format:").splitlines() if l.strip())
    hbad = sorted(f for f in hist if f.lower().endswith(SUFFIXES)
                  or any(h in Path(f).name.lower() for h in NAME_HINTS))
    check("no-data-files-in-history", not hbad, "历史中的数据文件 %d 个 %s" % (len(hbad), hbad[:5]))

    local = sorted(f for f in files if any(f.startswith(d + "/") for d in LOCAL_DIRS))
    check("local-dirs-untracked", not local, "被跟踪的本地文件 %d 个 %s" % (len(local), local[:5]))

    def leaks_path(text):
        if any(p.search(text) for p in ABS_PATTERNS):
            return True
        for name in ABS_USER_RE.findall(text):
            if name.lower() not in PLACEHOLDER_USERS:
                return True
        return False

    pathy = [f for f in content_files if leaks_path(blob(f))]
    check("no-absolute-local-paths", not pathy, "含本机绝对路径 %d 个 %s" % (len(pathy), pathy[:5]))

    secrets = [f for f in content_files
               if any(m and not SENTINEL_RE.search(m.group(0))
                      for m in SECRET_RE.finditer(blob(f)))]
    check("no-secret-literals", not secrets, "疑似密钥 %d 个 %s" % (len(secrets), secrets[:5]))

    leaky = [f for f in content_files if any(s in blob(f) for s in FORBIDDEN)]
    check("no-extraction-method-in-tree", not leaky, "含取钥方法描述 %d 个 %s" % (len(leaky), leaky[:5]))

    for name, rel in (("license", "LICENSE"), ("disclaimer", "DISCLAIMER.md")):
        check("has-" + name, (ROOT / rel).is_file(), rel)
    readme = blob("README.md")
    check("readme-has-privacy-section", "隐私" in readme and "边界" in readme,
          "README 需含「数据与隐私边界」小节")

    if args.json:
        print(json.dumps(out, ensure_ascii=False, indent=2))
    else:
        for r in out:
            print("[%s] %-34s %s" % ("PASS" if r["ok"] else "FAIL", r["check"], r["detail"]))
    failed = [r for r in out if not r["ok"]]
    print("\n%d/%d 项通过" % (len(out) - len(failed), len(out)))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
