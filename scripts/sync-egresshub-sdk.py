#!/usr/bin/env python3
"""Refresh the exact EgressHub SDK source snapshot; never copies runtime files."""
import argparse,hashlib,json,pathlib
p=argparse.ArgumentParser();p.add_argument('source',type=pathlib.Path);a=p.parse_args()
root=pathlib.Path(__file__).resolve().parents[1];dest=root/'third_party/egresshub'
files=['go.mod','client/client.go','client/policy.go','client/client_test.go','internal/credentials/load.go','internal/credentials/load_test.go']
hashes={}
for name in files:
 data=(a.source/name).read_bytes();target=dest/name;target.parent.mkdir(parents=True,exist_ok=True);target.write_bytes(data);hashes[name]=hashlib.sha256(data).hexdigest()
(dest/'SDK-SNAPSHOT.json').write_text(json.dumps({'source':'EgressHub source snapshot; not a published upstream module','files':hashes},indent=2)+'\n')
print('Updated SDK snapshot:',len(files),'source files; no runtime configuration or credentials')
