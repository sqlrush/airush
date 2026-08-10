"""AirushError 异常树与 MCP 错误形状（spec-0.8 D3）。

码空间 SSOT 在 proto/errors.json（make generate 产出 error_codes_gen.py）；
skill 入口的"异常 → MCP 错误响应"中间件在 spec-1.9 落地时消费 to_mcp_error。
"""

from typing import Any

from airush_skills.error_codes_gen import ERROR_CODES


class AirushError(Exception):
    """全部 skill 异常的基类：必须携带注册错误码。"""

    def __init__(self, code: str, *, cause: str | None = None) -> None:
        if code not in ERROR_CODES:
            raise ValueError(f"未注册的错误码: {code}")
        self.code = code
        self.cause = cause  # 内部细节，仅进日志，不进对外 message
        super().__init__(code)

    @property
    def message(self) -> str:
        """面向用户的消息（注册表模板，禁运行时拼接内部细节）。"""
        return str(ERROR_CODES[self.code]["message"])

    def to_mcp_error(self) -> dict[str, Any]:
        """转 MCP 错误响应载荷（spec-1.9 中间件消费；trace_id 由调用侧注入）。"""
        return {"code": self.code, "message": self.message}


class UpstreamError(AirushError):
    """上游依赖失败（E5 域）。"""


class NotImplementedFeature(AirushError):
    """规则 6：未实现分支的显式出口。"""

    def __init__(self) -> None:
        super().__init__("AR_COMMON_NOT_IMPLEMENTED")
