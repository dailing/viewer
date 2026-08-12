"""`python -m plugins.configstore --kernel-ws ... [--db ...]`"""

from .configstore import ConfigStorePlugin

if __name__ == "__main__":
    ConfigStorePlugin().run()
