"""`python -m plugins.supervisor --kernel-ws ... --registry ...`"""

from .supervisor import SupervisorPlugin

if __name__ == "__main__":
    SupervisorPlugin().run()
