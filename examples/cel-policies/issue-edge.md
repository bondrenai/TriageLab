# [edge] Health checks time out at the edge gateway

After last week's configuration rollout, every health request from our edge
gateway fails. We intended to allow five seconds for a response, and the
upstream service typically answers within 120 milliseconds. The previous
deployment worked.

The attached fixture simulates the gateway's deadline decision
deterministically, without contacting a service. Run it with
`python3 app.py` from the fixture directory.

Expected: The gateway allows five seconds for a health response.
Actual: The deadline expires after five milliseconds.

Please reproduce the failure, investigate the configuration and program
together, and verify a correction without changing the simulated response
latency.
