// kill-backup-cursors.js
//
// Scans a sharded cluster or replica set for backup cursors and kills any that are found. Topology is auto-detected.
//
// Requirements:
//   Both kill-backup-cursors.js and connection-string.js must be downloaded and placed in the same directory.
//   This script has been tested with mongosh 2.8.3. Earlier versions are not guaranteed to work.
//
// Usage:
//   mongosh --nodb --eval "var mongoUri='mongodb://user:pass@host:port/?tls=true'; var dryRun=false" /path/to/kill-backup-cursors.js
//
// Variables (set via --eval):
//   mongoUri  — full connection string including credentials and options; e.g. mongodb://user:pass@mongos:27017/?tls=true&authSource=admin
//   dryRun    — (optional) true to log what would be killed without actually killing anything; defaults to false
//
// Required roles on the connecting user:
//   atlasAdmin    — covers $currentOp (allUsers)
//   killOpSession — covers killCursors
//
// Note: --nodb ensures that the script doesn't automatically connect to mongodb://localhost:27017. Instead, the script manages its own connection.

"use strict";

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

if (typeof mongoUri === "undefined" || !mongoUri) {
  throw new Error("mongoUri is required");
}
if (typeof dryRun === "undefined") {
  var dryRun = false;
}

load(__dirname + "/connection-string.js");
// Works around mongosh 2.8.3 not supporting ES2020 optional chaining (?.).
const parsedUri = new ConnectionString(mongoUri, { looseValidation: true });
const user = decodeURIComponent(parsedUri.username);
const pass = decodeURIComponent(parsedUri.password);

// ---------------------------------------------------------------------------
// Step 1: connect and detect topology
// ---------------------------------------------------------------------------

const redactedUri = mongoUri.replace(/:\/\/[^@]*@/, "://<credentials>@");
print(`Connecting to ${redactedUri}...`);
const conn = new Mongo(mongoUri);
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
  const uri = parsedUri.clone();
  if (uri.isSRV) {
    // Direct connections require "mongodb://"."
    uri.protocol = "mongodb:";
    // SRV implies TLS. Make it explicit since we're using "mongodb://".
    if (!uri.searchParams.has("tls")) {
      uri.searchParams.set("tls", "true");
    }
  }
  uri.hosts = [host];
  uri.searchParams.set("directConnection", "true");
  uri.searchParams.delete("replicaSet");
  return uri.toString();
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
