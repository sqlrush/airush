"""spec-0.8 T6：Python 错误树与 MCP 形状语义。"""

import pytest

from airush_skills.error_codes_gen import ERROR_CODES
from airush_skills.errors import AirushError, NotImplementedFeature


def test_registry_generated() -> None:
    assert len(ERROR_CODES) >= 15
    assert "AR_INTERNAL_ERROR" in ERROR_CODES


def test_mcp_error_shape() -> None:
    err = AirushError("AR_UPSTREAM_LLM_TIMEOUT", cause="httpx timeout 30s")
    payload = err.to_mcp_error()
    assert payload["code"] == "AR_UPSTREAM_LLM_TIMEOUT"
    assert "cause" not in payload  # 内部细节不出对外通道
    assert "httpx" not in str(payload)


def test_unregistered_code_rejected() -> None:
    with pytest.raises(ValueError):
        AirushError("AR_FAKE_NOPE")


def test_not_implemented_shortcut() -> None:
    assert NotImplementedFeature().code == "AR_COMMON_NOT_IMPLEMENTED"
