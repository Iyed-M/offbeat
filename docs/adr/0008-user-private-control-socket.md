# User-Private Control Socket

The daemon control socket directory must be owner-only (`0700`) and its socket must be owner read/write only (`0600`). Daemon startup fails if it cannot establish those permissions, so another local user cannot issue control requests or inspect daemon state.
