# Client times out even though the health endpoint is healthy

After a configuration change, our client fails on every health request. We intended
to allow five seconds for a response, and the downstream service typically responds
within 120 milliseconds. The previous deployment worked.

The attached application simulates the client's deadline decision deterministically
without contacting a service. Run it with `python3 app.py` from the fixture directory.
Please reproduce the failure, investigate the configuration and program together,
and verify a correction without changing the simulated response latency.
