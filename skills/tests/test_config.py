"""spec-0.7 T7：Python 配置基类语义与 Go 侧对齐（前缀 / 枚举 / 默认值）。"""

import pytest
from pydantic import ValidationError

from airush_skills.config import SkillSettings


def test_prefix_and_default(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("AIRUSH_SKILLS_LOG_LEVEL", raising=False)
    assert SkillSettings().log_level == "info"

    monkeypatch.setenv("AIRUSH_SKILLS_LOG_LEVEL", "debug")
    assert SkillSettings().log_level == "debug"


def test_invalid_enum_rejected(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("AIRUSH_SKILLS_LOG_LEVEL", "loud")
    with pytest.raises(ValidationError):
        SkillSettings()
