# Frontend manual loop

Build the UI with `npm run build`, then run `../../next-go/bin/viewerd --static dist` from this directory.
Without `--static`, `viewerd` serves the UI embedded when the Go binary was built.
For live UI iteration, run `npm run dev` and keep `viewerd` on gateway port `18730`.
Vite proxies same-origin `/ws` to `ws://127.0.0.1:18730` by default.
Set `VITE_VIEWER_GATEWAY_TARGET=ws://127.0.0.1:<port>` when using another gateway port.
Open the Vite URL for the manual terminal and Bus Inspector visual pass.
