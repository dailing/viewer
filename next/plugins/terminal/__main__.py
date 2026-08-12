"""`python -m plugins.terminal --kernel-ws ...`"""

from .terminal import TerminalPlugin

if __name__ == "__main__":
    TerminalPlugin().run()
