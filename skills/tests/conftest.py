"""集成测试 fixtures（spec-0.5 D3）：容器 session 级复用，数据隔离由用例自管。"""

from collections.abc import Iterator

import pytest


@pytest.fixture(scope="session")
def redis_url() -> Iterator[str]:
    """启动 Redis 容器（redis:7.4，与 testkit/dev-deps 版本一致）。"""
    from testcontainers.community.redis import RedisContainer

    with RedisContainer("redis:7.4") as container:
        host = container.get_container_host_ip()
        port = container.get_exposed_port(6379)
        yield f"redis://{host}:{port}/0"
