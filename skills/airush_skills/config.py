"""skill 服务配置基类（spec-0.7 D2）。

与 Go 侧 libs/config 语义对齐：AIRUSH_SKILLS_ 前缀、SecretStr 脱敏、
本地 .env 加载（生产由部署侧保证不存在 .env 文件，spec-0.10 镜像契约）。
skill 子包继承 SkillSettings 增补自有字段。
"""

from typing import Literal

from pydantic_settings import BaseSettings, SettingsConfigDict


class SkillSettings(BaseSettings):
    """全部 skill 服务的公共配置基类。"""

    model_config = SettingsConfigDict(
        env_prefix="AIRUSH_SKILLS_",
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    log_level: Literal["debug", "info", "warn", "error"] = "info"
