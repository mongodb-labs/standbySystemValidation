// Usage: mongosh "<uri>" check-file-sizes.js
// Requires the readAnyDatabase@admin Atlas role.
// Flags any collection data file or index file exceeding 187 GiB on disk (storageSize, not logical size). Works against a replica set or mongos.

const THRESHOLD_GiB = 187;
const THRESHOLD_BYTES = THRESHOLD_GiB * 1024 * 1024 * 1024;
// Don't skip "local" since "local.oplog.rs" can be large.
const SKIP_DBS = new Set(["admin", "config"]);

const hello = db.adminCommand({ hello: 1 });
const isSharded = hello.msg === "isdbgrid";

if (!isSharded && !hello.setName) {
  print("Error: standalone topology is not supported. Run against a replica set or mongos.");
  quit(1);
}

const listDbResult = db.adminCommand({ listDatabases: 1, nameOnly: true });
if (!listDbResult.ok) {
  print(`Error: listDatabases failed: ${listDbResult.errmsg}`);
  quit(1);
}
const dbNames = listDbResult.databases.map(d => d.name).filter(n => !SKIP_DBS.has(n));

let totalData = 0;
let totalIndexes = 0;
let totalCollsChecked = 0;       // unique collections (replica set) or shard-collection pairs (sharded)
let totalIndexesChecked = 0;

print(`Threshold: ${THRESHOLD_GiB} GiB  |  topology: ${isSharded ? "sharded" : `replica set (${hello.setName})`}`);
print("=".repeat(60));

if (!isSharded) {
  const label = hello.setName;
  print(`Replica set: ${label}`);

  for (const dbName of dbNames) {
    const targetDb = db.getSiblingDB(dbName);
    for (const coll of targetDb.getCollectionInfos({ type: "collection" })) {
      const stats = getCollStats(targetDb, coll.name);
      if (!stats) continue;

      const storageSize = stats.storageSize || 0;
      const indexSizes = stats.indexSizes || {};

      totalCollsChecked++;
      totalIndexesChecked += Object.keys(indexSizes).length;

      printCollection(`${dbName}.${coll.name}`, storageSize, indexSizes);
      const c = countFlags(storageSize, indexSizes);
      totalData += c.data;
      totalIndexes += c.indexes;
    }
  }

} else {
  // Collect per-shard results, then print grouped by shard.
  const byShards = {};

  for (const dbName of dbNames) {
    const targetDb = db.getSiblingDB(dbName);
    for (const coll of targetDb.getCollectionInfos({ type: "collection" })) {
      const stats = getCollStats(targetDb, coll.name);
      if (!stats || !stats.shards) continue;

      for (const [shardName, shardStats] of Object.entries(stats.shards)) {
        const storageSize = shardStats.storageSize || 0;
        const indexSizes = shardStats.indexSizes || {};

        totalCollsChecked++;
        totalIndexesChecked += Object.keys(indexSizes).length;

        if (!byShards[shardName]) byShards[shardName] = [];
        byShards[shardName].push({ ns: `${dbName}.${coll.name}`, storageSize, indexSizes });

        const c = countFlags(storageSize, indexSizes);
        totalData += c.data;
        totalIndexes += c.indexes;
      }
    }
  }

  for (const [shardName, entries] of Object.entries(byShards)) {
    print(`\nSHARD: ${shardName}`);
    print("-".repeat(40));
    for (const { ns, storageSize, indexSizes } of entries) {
      printCollection(ns, storageSize, indexSizes);
    }
  }
}

print("\n" + "=".repeat(60));
const collLabel = isSharded ? "shard-collection pair(s)" : "collection(s)";
print(`Checked: ${totalCollsChecked} ${collLabel}, ${totalIndexesChecked} index(es)`);
print(`Warned: ${totalData} oversized data file(s), ${totalIndexes} oversized index file(s)`);

function toGiB(bytes) {
  return (bytes / (1024 * 1024 * 1024)).toFixed(1);
}

function pad(str, width) {
  return String(str).padStart(width);
}

function printCollection(ns, storageSize, indexSizes) {
  const dataFlag = storageSize > THRESHOLD_BYTES ? "  [WARN]" : "";
  print(`\n  ${ns}`);
  print(`    data: ${pad(toGiB(storageSize), 8)} GiB${dataFlag}`);
  for (const [name, size] of Object.entries(indexSizes)) {
    const idxFlag = size > THRESHOLD_BYTES ? "  [WARN]" : "";
    print(`    ${name}: ${pad(toGiB(size), 8)} GiB${idxFlag}`);
  }
}

function getCollStats(targetDb, collName) {
  try {
    const result = targetDb.runCommand({ collStats: collName });
    if (!result.ok) {
      print(`  [WARN] collStats failed for ${targetDb.getName()}.${collName}: ${result.errmsg}`);
      return null;
    }
    return result;
  } catch (e) {
    print(`  [WARN] collStats failed for ${targetDb.getName()}.${collName}: ${e.message}`);
    return null;
  }
}

function countFlags(storageSize, indexSizes) {
  return {
    data: storageSize > THRESHOLD_BYTES ? 1 : 0,
    indexes: Object.values(indexSizes).filter(s => s > THRESHOLD_BYTES).length,
  };
}
