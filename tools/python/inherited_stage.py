"""Create atomic-output staging directories without restricting Windows ACLs."""

from __future__ import annotations

import os
from pathlib import Path
import secrets


def create_inherited_stage(output: Path) -> Path:
    """Create a sibling staging directory that inherits its parent's access.

    Python 3.13 gives ``tempfile.mkdtemp`` directories created with mode 0700 a
    deliberately restricted Windows ACL.  Renaming such a directory into place
    preserves that ACL, which is unsuitable for generated, shared development
    data.  ``os.mkdir`` without the special 0700 mode inherits the parent ACL on
    Windows and retains the normal umask-controlled behaviour on POSIX.
    """
    output = Path(output)
    output.parent.mkdir(parents=True, exist_ok=True)
    prefix = f".{output.name}."
    for _ in range(100):
        stage = output.parent / f"{prefix}{secrets.token_hex(8)}"
        try:
            os.mkdir(stage)
        except FileExistsError:
            continue
        return stage
    raise FileExistsError(f"unable to create a unique staging directory for {output}")
