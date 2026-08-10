"""spec-0.5 D5 冒烟：Python 侧集成链路（Redis 容器读写）。"""

import pytest

pytestmark = pytest.mark.integration


def test_redis_set_get(redis_url: str) -> None:
    import redis

    client = redis.Redis.from_url(redis_url)
    assert client.set("probe", "from-python") is True
    assert client.get("probe") == b"from-python"
