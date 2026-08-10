"""spec-0.1 T2/T4 冒烟：包可 import 且版本形态正确（TDD 先行，红→绿）。"""

from airush_skills import __version__


def test_package_importable() -> None:
    assert __version__.count(".") == 2
