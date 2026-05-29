// Usage: mongosh "<uri>" check-file-sizes.js
// Requires the readAnyDatabase@admin Atlas role.
// Flags any collection data file or index file exceeding 187 GiB on disk (storageSize, not logical size). Works against a replica set or mongos.

const THRESHOLD_GiB = 187;
const THRESHOLD_BYTES = THRESHOLD_GiB * 1024 * 1024 * 1024;
const SKIP_DBS = new Set(["admin", "config", "local"]);

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
  for (const dbName of dbNames) {
    const targetDb = db.getSiblingDB(dbName);
    for (const coll of targetDb.getCollectionInfos({ type: "collection" })) {
      const docs = getCollStats(targetDb, coll.name);
      if (!docs) continue;

      // Index 0 because there's no per-shard split.
      const storageSize = docs[0].storageStats.storageSize || 0;
      const indexSizes = docs[0].storageStats.indexSizes || {};

      totalCollsChecked++;
      totalIndexesChecked += Object.keys(indexSizes).length;

      printWarnIfOver(`${dbName}.${coll.name}`, storageSize, indexSizes);
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
      const docs = getCollStats(targetDb, coll.name);
      if (!docs) continue;

      for (const doc of docs) {
        const shardName = doc.shard;
        const storageSize = doc.storageStats.storageSize || 0;
        const indexSizes = doc.storageStats.indexSizes || {};

        totalCollsChecked++;
        totalIndexesChecked += Object.keys(indexSizes).length;

        if (!byShards[shardName]) byShards[shardName] = { collCount: 0, indexCount: 0, entries: [] };
        byShards[shardName].collCount++;
        byShards[shardName].indexCount += Object.keys(indexSizes).length;
        byShards[shardName].entries.push({ ns: `${dbName}.${coll.name}`, storageSize, indexSizes });

        const c = countFlags(storageSize, indexSizes);
        totalData += c.data;
        totalIndexes += c.indexes;
      }
    }
  }

  for (const [shardName, { collCount, indexCount, entries }] of Object.entries(byShards)) {
    print(`\nSHARD: ${shardName}  |  checked ${collCount} collection(s), ${indexCount} index(es)`);
    print("-".repeat(40));
    for (const { ns, storageSize, indexSizes } of entries) {
      printWarnIfOver(ns, storageSize, indexSizes);
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

function printWarnIfOver(ns, storageSize, indexSizes) {
  const dataOver = storageSize > THRESHOLD_BYTES;
  const overIndexes = Object.entries(indexSizes).filter(([, s]) => s > THRESHOLD_BYTES);
  if (!dataOver && overIndexes.length === 0) return;

  print(`\n  ${ns}`);
  if (dataOver) print(`    data: ${pad(toGiB(storageSize), 8)} GiB  [WARN]`);
  for (const [name, size] of overIndexes) {
    print(`    ${name}: ${pad(toGiB(size), 8)} GiB  [WARN]`);
  }
}

function getCollStats(targetDb, collName) {
  try {
    const docs = targetDb.getCollection(collName).aggregate([
      { $collStats: { storageStats: {} } }
    ]).toArray();
    return docs.length ? docs : null;
  } catch (e) {
    print(`  [WARN] $collStats failed for ${targetDb.getName()}.${collName}: ${e.message}`);
    return null;
  }
}

function countFlags(storageSize, indexSizes) {
  return {
    data: storageSize > THRESHOLD_BYTES ? 1 : 0,
    indexes: Object.values(indexSizes).filter(s => s > THRESHOLD_BYTES).length,
  };
}
