import pytest

import vigil.git_status
import vigil.pr_status


@pytest.fixture(autouse=True)
def _clear_caches():
    """Clear module-level caches between tests."""
    vigil.git_status._default_branch_cache.clear()
    vigil.pr_status._nwo_cache.clear()
    yield
    vigil.git_status._default_branch_cache.clear()
    vigil.pr_status._nwo_cache.clear()
