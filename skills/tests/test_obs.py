"""spec-0.9 T7：Python 日志字段 schema 与 Go 侧一致 + redaction。"""

import json
from typing import Any

import structlog

from airush_skills.obs import setup_logging


def _capture() -> list[str]:
    lines: list[str] = []

    class Sink:
        def msg(self, message: str) -> None:
            lines.append(message)

        log = debug = info = warn = warning = error = msg

    structlog.configure(logger_factory=lambda *a: Sink())
    return lines


def _last_record(lines: list[str]) -> dict[str, Any]:
    assert lines, "no log emitted"
    return dict(json.loads(lines[-1]))


def test_base_fields_schema() -> None:
    setup_logging("skills")
    lines = _capture()
    structlog.get_logger().info("hello")

    rec = _last_record(lines)
    assert rec["component"] == "skills"
    assert rec["tenant_id"] == "-"
    assert rec["trace_id"] == "-"


def test_redaction_masks_secrets() -> None:
    setup_logging("skills")
    lines = _capture()
    structlog.get_logger().info("connect", detail="password=hunter2 dsn=postgres://u:s3cret@h/db")

    raw = lines[-1]
    assert "hunter2" not in raw
    assert "s3cret" not in raw
    assert "***REDACTED***" in raw


def test_explicit_trace_id_not_overwritten() -> None:
    setup_logging("skills")
    lines = _capture()
    structlog.get_logger().info("evt", trace_id="tr_from_mcp")

    assert _last_record(lines)["trace_id"] == "tr_from_mcp"
