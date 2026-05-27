// kill-backup-cursors.js
//
// Scans a sharded cluster or replica set for backup cursors and kills any that are found. Topology is auto-detected.
//
// Usage:
//   mongosh --nodb \
//     --eval "var mongoUri='mongodb://host:port'; var user='u'; var pass='p'; var useTls=false; var dryRun=false" \
//     /path/to/kill-backup-cursors.js
//
// Variables (set via --eval):
//   mongoUri  — mongos URI for sharded clusters, replica set URI for replica sets; don't specify credentials in the URI
//   user      — MongoDB username
//   pass      — MongoDB password
//   useTls       — true/false (please set to true for Atlas clusters)
//   dryRun    — (optional) true to log what would be killed without actually killing anything; defaults to false
//
// Required roles on the connecting user:
//   atlasAdmin    — covers $currentOp (allUsers)
//   killOpSession — covers killCursors
//
// Note: this has been tested on Atlas clusters. This may not work if a MongoDB deployment has custom TLS / auth settings.

"use strict";

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

if (typeof mongoUri === "undefined" || !mongoUri) {
  throw new Error("mongoUri is required");
}
if (typeof user === "undefined" || !user) {
  throw new Error("user is required");
}
if (typeof pass === "undefined" || !pass) {
  throw new Error("pass is required");
}
if (typeof useTls === "undefined") {
  throw new Error("useTls is required (true or false)");
}
if (typeof dryRun === "undefined") {
  var dryRun = false;
}

// ---------------------------------------------------------------------------
// Step 1: connect and detect topology
// ---------------------------------------------------------------------------

const redactedUri = mongoUri.replace(/:\/\/[^@]*@/, "://<credentials>@");
print(`Connecting to ${redactedUri}...`);
const effectiveUri = useTls
  ? mongoUri + (mongoUri.includes("?") ? "&" : "?") + "tls=true"
  : mongoUri;
const conn = new Mongo(effectiveUri);
const adminDb = conn.getDB("admin");
if (!adminDb.auth(user, pass)) {
  throw new Error("Authentication failed for user: " + user);
}
print("Connected.");

print("Detecting topology...");
// See https://www.mongodb.com/docs/manual/reference/command/hello/#mongodb-data-hello for more info.
const hello = adminDb.adminCommand({ hello: 1 });
const isSharded = hello.msg === "isdbgrid";
const isReplSet = !!hello.setName;

if (!isSharded && !isReplSet) {
  print("FATAL: unsupported topology (standalone not supported)");
  quit(1);
}
print(`Topology: ${isSharded ? "sharded cluster" : `replica set (${hello.setName})`}`);

// ---------------------------------------------------------------------------
// Step 2: collect all mongod hosts to scan
//
// Sharded:     getShardMap returns every replica set (config + all shards) as
//              "rsName/h1:port,h2:port,..." — we expand each into individual hosts.
// Replica set: replSetGetStatus gives us all members including hidden ones.
// ---------------------------------------------------------------------------

let hosts;

if (isSharded) {
  // See https://www.mongodb.com/docs/manual/reference/command/getShardMap/ for more info.
  const shardMap = adminDb.adminCommand({ getShardMap: 1 });
  if (!shardMap.ok) {
    print("FATAL: getShardMap failed: " + JSON.stringify(shardMap));
    quit(1);
  }
  const allHosts = [];
  for (const connStr of Object.values(shardMap.map)) {
    allHosts.push(...parseHostList(connStr));
  }
  hosts = [...new Set(allHosts)];
  print(`Found ${hosts.length} mongod(s) across all shards and config server: ${hosts.join(", ")}`);
} else {
  const rsStatus = adminDb.adminCommand({ replSetGetStatus: 1 });
  if (!rsStatus.ok) {
    print("FATAL: replSetGetStatus failed: " + JSON.stringify(rsStatus));
    quit(1);
  }
  hosts = rsStatus.members.map(m => m.name);
  print(`Replica set has ${hosts.length} member(s): ${hosts.join(", ")}`);
}

// ---------------------------------------------------------------------------
// Step 3: scan each host directly and kill any backup cursors found
// ---------------------------------------------------------------------------

// { host -> { killed: [cursorId], skipped: [cursorId], error: string } }
const results = {};

for (const host of hosts) {
  results[host] = { killed: [], skipped: [], error: "" };
  const entry = results[host];

  print(`[${host}]  Connecting...`);
  let nodeDb;
  try {
    const nodeConn = new Mongo(directUri(host));
    nodeDb = nodeConn.getDB("admin");
    if (!nodeDb.auth(user, pass)) {
      throw new Error("authentication failed for user: " + user);
    }
  } catch (e) {
    entry.error = "connection failed: " + e.message;
    print(`[${host}]  ERROR: ${entry.error}`);
    continue;
  }

  print(`[${host}]  Scanning for backup cursors...`);
  let cursors;
  try {
    // writeConcern/readConcern must be explicitly passed (empty) for $currentOp to work on config server nodes.
    const res = nodeDb.runCommand({
      aggregate: 1,
      pipeline: backupCursorPipeline(),
      cursor: {},
      writeConcern: {},
      readConcern: {},
    });
    if (!res.ok) throw new Error("aggregate returned ok:0: " + JSON.stringify(res));
    cursors = res.cursor.firstBatch;
  } catch (e) {
    entry.error = "$currentOp failed: " + e.message;
    print(`[${host}]  ERROR: ${entry.error}`);
    continue;
  }

  if (cursors.length === 0) {
    print(`[${host}]  No backup cursor found.`);
    continue;
  }

  for (const { cursorId } of cursors) {
    if (dryRun) {
      entry.skipped.push(cursorId);
      print(`[${host}]  DRY RUN: would kill cursorId=${cursorId}`);
      continue;
    }

    print(`[${host}]  Killing cursorId=${cursorId}...`);
    try {
      killCursor(nodeDb, host, cursorId);
      entry.killed.push(cursorId);
      print(`[${host}]  Killed cursorId=${cursorId}.`);
    } catch (e) {
      entry.error += `killCursors(${cursorId}) failed: ${e.message}; `;
      print(`[${host}]  ERROR: ${e.message}`);
    }
  }
}

// ---------------------------------------------------------------------------
// Step 4: summary
// ---------------------------------------------------------------------------

const divider = "=".repeat(60);
print("\n" + divider);
print(dryRun ? "=== Backup Cursor Scan (DRY RUN — nothing killed)" : "=== Backup Cursor Kill Summary");
print(divider);

let totalFound = 0;
let totalKilled = 0;
let totalErrors = 0;

for (const [host, r] of Object.entries(results)) {
  const found = r.killed.length + r.skipped.length;
  totalFound += found;
  totalKilled += r.killed.length;
  if (r.error) totalErrors++;

  if (r.error) {
    print(`[${host}]  ERROR: ${r.error}`);
  } else if (found === 0) {
    print(`[${host}]  no backup cursor`);
  } else {
    const ids = dryRun ? r.skipped : r.killed;
    const action = dryRun ? "would kill" : "killed";
    for (const cursorId of ids) {
      print(`[${host}]  ${action} cursorId=${cursorId}`);
    }
  }
}

print(divider);
print(`Cursors found:  ${totalFound}`);
if (dryRun) {
  print(`Cursors that would be killed: ${totalFound}`);
} else {
  print(`Cursors killed: ${totalKilled}`);
}
if (totalErrors > 0) {
  print(`Nodes with errors: ${totalErrors}`);
}
print(divider + "\n");

quit(totalErrors > 0 ? 1 : 0);

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function directUri(host) {
  const tlsParam = useTls ? "&tls=true" : "";
  return `mongodb://${host}/?directConnection=true&authSource=admin${tlsParam}`;
}

function backupCursorPipeline() {
  return [
    { $currentOp: { allUsers: true, idleCursors: true } },
    { $match: { ns: "admin.$cmd.aggregate" } },
    {
      $addFields: {
        pipelineKeys: {
          $map: {
            input: "$cursor.originatingCommand.pipeline",
            as: "stage",
            in: { $objectToArray: "$$stage" },
          },
        },
      },
    },
    {
      $match: {
        pipelineKeys: {
          $elemMatch: { $elemMatch: { k: "$backupCursor" } },
        },
      },
    },
    {
      $project: {
        cursorId: "$cursor.cursorId",
        _id: 0,
      },
    },
  ];
}

function killCursor(adminDb, host, cursorId) {
  const res = adminDb.runCommand({ killCursors: "$cmd.aggregate", cursors: [cursorId] });
  if (!res.ok) {
    throw new Error("killCursors returned ok:0 — " + JSON.stringify(res));
  }
  const killed = res.cursorsKilled || [];
  const confirmed = killed.some(id => id.toString() === cursorId.toString());
  if (!confirmed) {
    throw new Error(`cursorId ${cursorId} not found in cursorsKilled: ${JSON.stringify(killed)}`);
  }
}

// Parse "replicaSetName/h1:port,h2:port,..." or bare "h1:port,h2:port,..." into host:port strings.
function parseHostList(connStr) {
  const hostsPart = connStr.includes("/") ? connStr.split("/")[1] : connStr;
  return hostsPart.split(",").map(h => h.trim()).filter(Boolean);
}
