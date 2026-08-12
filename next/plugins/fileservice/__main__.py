"""`python -m plugins.fileservice --kernel-ws ...`"""

from .fileservice import FileServicePlugin

if __name__ == "__main__":
    FileServicePlugin().run()
