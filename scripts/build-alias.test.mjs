import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { once } from "node:events";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const updateScript = fileURLToPath(new URL("./update-wacli-alias.sh", import.meta.url));

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "whatscli-alias-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.mkdirSync(path.join(root, "dist"));
  return root;
}

function updateAlias(root) {
  return spawnSync("bash", [updateScript], { cwd: root, encoding: "utf8" });
}

test("updating the alias preserves a running legacy owner and executable permissions", {
  skip: process.platform !== "linux",
}, async (t) => {
  const root = fixture(t);
  const primary = path.join(root, "dist", "whatscli");
  const alias = path.join(root, "dist", "wacli");
  fs.copyFileSync("/bin/cat", alias);
  fs.copyFileSync("/bin/echo", primary);
  fs.chmodSync(alias, 0o755);
  fs.chmodSync(primary, 0o755);
  const owner = spawn(alias, [], { stdio: ["pipe", "ignore", "pipe"] });
  const exited = once(owner, "exit");
  await once(owner, "spawn");
  try {
    const result = updateAlias(root);
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(fs.readFileSync(alias), fs.readFileSync(primary));
    assert.equal(fs.statSync(alias).mode & 0o777, 0o755);
    assert.equal(owner.exitCode, null);
    assert.deepEqual(fs.readdirSync(path.join(root, "dist")).sort(), ["wacli", "whatscli"]);
  } finally {
    owner.stdin.end();
    const [code] = await exited;
    assert.equal(code, 0);
  }
});

test("a failed alias update keeps the previous binary and removes its temporary file", {
  skip: process.platform === "win32",
}, (t) => {
  const root = fixture(t);
  const alias = path.join(root, "dist", "wacli");
  fs.writeFileSync(alias, "previous binary");
  const result = updateAlias(root);
  assert.notEqual(result.status, 0);
  assert.equal(fs.readFileSync(alias, "utf8"), "previous binary");
  assert.deepEqual(fs.readdirSync(path.join(root, "dist")), ["wacli"]);
});
