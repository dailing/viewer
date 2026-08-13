# pingpong

该示例创建 `ping` 和 `pong` 两个 `busclient.Client`，两边各暴露一个 RPC channel，然后相互发起请求。

先启动内核，再运行：

```bash
export PATH="$HOME/.local/go/bin:$PATH"
go run ./examples/pingpong -url ws://127.0.0.1:8765/ws
```

预期输出：

```text
pong received ping
ping received pong
```
