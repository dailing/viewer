"""`python -m plugins.inspector --kernel-ws ... [--echo]`"""

from .inspector import BusInspectorPlugin

if __name__ == "__main__":
    BusInspectorPlugin().run()
