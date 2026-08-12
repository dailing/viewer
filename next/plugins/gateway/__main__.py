"""`python -m plugins.gateway --kernel-ws ... [--host --port --static]`"""

from .gateway import HttpGatewayPlugin

if __name__ == "__main__":
    HttpGatewayPlugin().run()
