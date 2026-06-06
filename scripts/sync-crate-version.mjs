// Keep the Rust SDK crate's version lockstep with the kit version (single
// authority: changesets -> package.json). Chained after `changeset version`
// in the package.json "version" script so the Version Packages PR carries the
// crate bump; CI asserts the two stay equal (rust job).
import { readFileSync, writeFileSync } from "node:fs";

const version = JSON.parse(readFileSync("package.json", "utf8")).version;
const path = "rust/Cargo.toml";
const toml = readFileSync(path, "utf8");
const next = toml.replace(/^version = ".*"$/m, `version = "${version}"`);
if (next === toml && !toml.includes(`version = "${version}"`)) {
  console.error(`sync-crate-version: could not find version line in ${path}`);
  process.exit(1);
}
writeFileSync(path, next);
console.log(`sync-crate-version: rust/Cargo.toml -> ${version}`);
