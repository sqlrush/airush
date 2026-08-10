"""spec-0.4 D5：pytest 范本——参数化 + async（asyncio_mode=auto 免装饰器）。"""

import asyncio

import pytest

from airush_skills import __version__


@pytest.mark.parametrize(
    ("value", "expected_parts"),
    [
        ("0.0.0", 3),
        (__version__, 3),
    ],
)
def test_version_has_three_parts(value: str, expected_parts: int) -> None:
    assert len(value.split(".")) == expected_parts


async def test_async_smoke() -> None:
    await asyncio.sleep(0)
