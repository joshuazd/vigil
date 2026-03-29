from __future__ import annotations

import logging
import logging.handlers
import os
from pathlib import Path

LOG_DIR = Path.home() / ".local" / "share" / "vigil"
LOG_FILE = LOG_DIR / "vigil.log"


def configure() -> None:
    """Set up rotating file logger. Call once from main()."""
    LOG_DIR.mkdir(parents=True, exist_ok=True)
    level = os.environ.get("VIGIL_LOG_LEVEL", "INFO").upper()
    handler = logging.handlers.RotatingFileHandler(
        LOG_FILE, maxBytes=2 * 1024 * 1024, backupCount=2,
    )
    handler.setFormatter(logging.Formatter("%(asctime)s %(levelname)s %(name)s %(message)s"))
    root = logging.getLogger("vigil")
    root.setLevel(getattr(logging, level, logging.INFO))
    root.addHandler(handler)
