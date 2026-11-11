# Session cache rejects writes once it fills up

We moved checkout sessions to a dedicated Redis cache. In production it is
capped well above the working set; the attached fixture uses the same settings
scaled down (a 1 MB cap, per `redis.conf`) so the failure shows up within one
warm-up. It is configured with an LRU eviction policy, so we expected Redis to
rotate out the
oldest sessions when the cap is reached. Instead, about halfway through the
nightly warm-up every write starts failing with
`OOM command not allowed when used memory > 'maxmemory'`, and checkout returns
errors until someone flushes the cache.

The attached fixture reproduces the warm-up against a local Redis that uses the
production settings in `redis.conf`. It needs `redis-server` and `redis-cli` and
no network access. Run it with `bash warmup.sh` from the fixture directory.
Please reproduce the failure, explain why eviction is not happening, and verify
a correction without raising the memory cap or shrinking the warm-up.
