import assert from "node:assert/strict";
import test from "node:test";
import { classifyGovulncheckEvents, formatGateResult } from "./govulncheck-stdlib.mjs";

const finding = (osv, ...trace) => ({ finding: { osv, trace } });

test("third-party vulnerabilities with standard-library callers remain reported without failing the stdlib gate", () => {
  const result = classifyGovulncheckEvents([
    finding("GO-HTTP2", { module: "golang.org/x/net", package: "golang.org/x/net/http2", function: "RoundTrip" },
      { module: "stdlib", package: "net/http", function: "Do" }),
  ]);
  assert.deepEqual(result.stdlib, []);
  assert.deepEqual(result.thirdParty[0].modules, ["golang.org/x/net"]);
  assert.match(formatGateResult(result), /NOTICE non-stdlib findings remain reported and unsuppressed: GO-HTTP2/);
});

test("standard-library vulnerabilities still block when called by third-party code", () => {
  const result = classifyGovulncheckEvents([
    finding("GO-TLS", { module: "stdlib", package: "crypto/tls", function: "Handshake" },
      { module: "example.com/client", package: "example.com/client", function: "Connect" }),
  ]);
  assert.deepEqual(result.stdlib.map(({ id }) => id), ["GO-TLS"]);
  assert.deepEqual(result.thirdParty, []);
  assert.doesNotMatch(formatGateResult(result), /gate clean/);
});

test("an advisory affecting several modules keeps reachability specific to each vulnerable module", () => {
  const result = classifyGovulncheckEvents([
    finding("GO-HTTP2", { module: "stdlib", package: "net/http" }),
    finding("GO-HTTP2", { module: "golang.org/x/net", package: "golang.org/x/net/http2", function: "RoundTrip" },
      { module: "stdlib", package: "net/http", function: "Do" }),
  ]);
  assert.deepEqual(result.stdlib, []);
  assert.deepEqual(result.stdlibNonReachable.map(({ id }) => id), ["GO-HTTP2"]);
  assert.deepEqual([...result.active[0].moduleLevels.get("stdlib")], ["package"]);
  const reachable = classifyGovulncheckEvents([
    finding("GO-HTTP2", { module: "stdlib", package: "net/http", function: "RoundTrip" }),
    finding("GO-HTTP2", { module: "golang.org/x/net", package: "golang.org/x/net/http2", function: "RoundTrip" }),
  ]);
  assert.deepEqual(reachable.stdlib.map(({ id }) => id), ["GO-HTTP2"]);
  assert.deepEqual(reachable.thirdParty.map(({ id }) => id), ["GO-HTTP2"]);
});

test("module-only findings do not imply reachable standard-library symbols", () => {
  const result = classifyGovulncheckEvents([finding("GO-TLS", { module: "stdlib" })]);
  assert.deepEqual(result.stdlib, []);
  assert.deepEqual(result.stdlibNonReachable.map(({ id }) => id), ["GO-TLS"]);
});

test("missing vulnerable-module information fails closed even with classified findings or callers", () => {
  const result = classifyGovulncheckEvents([
    finding("GO-UNKNOWN", { package: "unknown", function: "Target" },
      { module: "stdlib", package: "fmt", function: "Print" }),
    finding("GO-UNKNOWN", { module: "example.com/client", function: "Target" }),
    finding("GO-EMPTY"),
  ]);
  assert.deepEqual(result.unclassified.map(({ id }) => id), ["GO-EMPTY", "GO-UNKNOWN"]);
  assert.doesNotMatch(formatGateResult(result), /gate clean/);
});
