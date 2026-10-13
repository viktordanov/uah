"""SUMMARY.json maps each service to its count of incidents that mention timeout."""
import json
import sys

want = json.load(open("expected.json"))
try:
    got = json.load(open("SUMMARY.json"))
except (FileNotFoundError, ValueError) as e:
    sys.exit("SUMMARY.json: %s" % e)
if got != want:
    sys.exit("SUMMARY.json is %s, want %s" % (got, want))
