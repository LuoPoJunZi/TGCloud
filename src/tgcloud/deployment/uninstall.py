"""Module entry point for the host uninstall command."""

import signal

from tgcloud.deployment.lifecycle.uninstall import main

if __name__ == "__main__":
    def interrupted(_signum, _frame):
        raise KeyboardInterrupt

    for signal_name in ("SIGTERM", "SIGHUP"):
        if hasattr(signal, signal_name):
            signal.signal(getattr(signal, signal_name), interrupted)
    raise SystemExit(main())
