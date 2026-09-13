# JSON Lines Control Protocol

The CLI and daemon communicate through a versioned JSON Lines request-response protocol over the Unix domain socket, with one request per connection. A request is `{ "version": 1, "command": "..." }`; a response is either `{ "version": 1, "result": ... }` or `{ "version": 1, "error": ... }`. Strict Go schemas, maximum message sizes, explicit version rejection, and structured errors keep the local contract evolvable without introducing HTTP for the small trusted control surface.
