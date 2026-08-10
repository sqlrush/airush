"""skill 服务观测基线：structlog JSON 日志（spec-0.9 D2）。

与 Go 侧 libs/obs 字段 schema 对齐：component / tenant_id / trace_id 必带
（缺上下文为 "-"），redaction 模式清单与 Go 侧同步维护。
OTel trace/metric 导出随 skill 首个服务进程在 spec-1.9 接入（spec 修订记录）。
"""

import logging
import re
from collections.abc import MutableMapping
from typing import Any

import structlog

_REDACT_PATTERNS = [
    re.compile(r"(?i)(password|passwd|pwd)=[^\s&\"']+"),
    re.compile(r"(?i)bearer\s+[A-Za-z0-9._\-]+"),
    re.compile(r"AKIA[0-9A-Z]{16}"),
    re.compile(r"(?i)(sk|ghp|gho|xox[bp])-[A-Za-z0-9\-_]{16,}"),
    re.compile(r"postgres(ql)?://[^:\s]+:[^@\s]+@"),
]
_REDACTED = "***REDACTED***"

_LEVELS = {
    "debug": logging.DEBUG,
    "info": logging.INFO,
    "warn": logging.WARNING,
    "error": logging.ERROR,
}


def _redact(_: Any, __: str, event_dict: MutableMapping[str, Any]) -> MutableMapping[str, Any]:
    """record 级打码（全部字符串值，含 event 消息本身）。"""
    for key, value in event_dict.items():
        if isinstance(value, str):
            masked = value
            for pattern in _REDACT_PATTERNS:
                masked = pattern.sub(_REDACTED, masked)
            if masked != value:
                event_dict[key] = masked
    return event_dict


def _base_fields(component: str) -> Any:
    def processor(
        _: Any, __: str, event_dict: MutableMapping[str, Any]
    ) -> MutableMapping[str, Any]:
        event_dict.setdefault("component", component)
        event_dict.setdefault("tenant_id", "-")
        event_dict.setdefault("trace_id", "-")
        return event_dict

    return processor


def setup_logging(component: str, level: str = "info") -> None:
    """配置全局 structlog（skill 进程入口调用一次）。"""
    structlog.configure(
        processors=[
            structlog.contextvars.merge_contextvars,
            structlog.processors.add_log_level,
            structlog.processors.TimeStamper(fmt="iso", key="time"),
            _base_fields(component),
            _redact,
            structlog.processors.JSONRenderer(),
        ],
        wrapper_class=structlog.make_filtering_bound_logger(_LEVELS.get(level, logging.INFO)),
        cache_logger_on_first_use=True,
    )


def get_logger() -> structlog.stdlib.BoundLogger:
    """取全局 logger（setup_logging 之后使用）。"""
    return structlog.get_logger()  # type: ignore[no-any-return]
