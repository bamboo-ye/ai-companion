#!/bin/sh
set -eu

PYTHON=${PYTHON:-workers/python/.venv/bin/python}
PYTHONPATH=${PYTHONPATH:-workers/python/src}
export PYTHONPATH

exec "$PYTHON" -m ai_companion_worker.evaluation.direct_canary "$@"
