"""`python -m plugins.instancestore --kernel-ws ... [--db ...]`"""

from .instancestore import InstanceStorePlugin

if __name__ == "__main__":
    InstanceStorePlugin().run()
