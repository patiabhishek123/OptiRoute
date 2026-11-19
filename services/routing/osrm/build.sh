#!/usr/bin/env bash
# Build the OSRM (MLD, car profile) dataset for one region.
#
#   services/routing/osrm/build.sh            # Odisha (default)
#
# Steps: download the Geofabrik zone (MD5-verified) -> clip it to the region's bounding box with
# osmium -> osrm-extract / partition / customize. Every Docker step runs with hard memory and CPU
# caps and low priority, so preprocessing cannot exhaust the laptop: if a step needs more than
# MEM it is OOM-killed, rather than the machine starting to swap. Re-running skips finished steps.
set -euo pipefail

REGION=${REGION:-odisha}
ZONE_URL=${ZONE_URL:-https://download.geofabrik.de/asia/india/eastern-zone-latest.osm.pbf}
BBOX=${BBOX:-81.3,17.7,87.6,22.6}  # min_lon,min_lat,max_lon,max_lat (Odisha + small margin)
OSRM_IMAGE=${OSRM_IMAGE:-ghcr.io/project-osrm/osrm-backend:v6.0.0}
MEM=${MEM:-3g}    # per-container memory cap
CPUS=${CPUS:-4}   # leave half the cores free

here=$(cd "$(dirname "$0")" && pwd)
data="$here/data"
mkdir -p "$data"
zone="$data/$(basename "$ZONE_URL")"
clip="$data/$REGION.osm.pbf"

run() {  # docker run with resource caps, data dir mounted at /data
  nice -n 10 docker run --rm --memory "$MEM" --memory-swap "$MEM" --cpus "$CPUS" \
    -u "$(id -u):$(id -g)" -v "$data:/data" "$@"
}

if [[ ! -f "$zone" ]]; then
  echo ">> downloading $(basename "$ZONE_URL")"
  curl -fL --retry 3 -o "$zone.part" "$ZONE_URL"
  curl -fsSL -o "$zone.md5" "$ZONE_URL.md5"
  (cd "$data" && sed "s/$(basename "$ZONE_URL")/$(basename "$zone").part/" "$zone.md5" | md5sum -c -)
  mv "$zone.part" "$zone"
fi

if [[ ! -f "$clip" ]]; then
  echo ">> clipping to $REGION bbox $BBOX"
  # --network host: Docker build containers cannot resolve DNS on hosts that use
  # systemd-resolved (127.0.0.53); the build only needs to reach the Debian mirrors.
  docker build --network host -q -t optiroute/osmium -f "$here/osmium.Dockerfile" "$here" >/dev/null
  # "simple" is single-pass and light on memory: complete_ways was OOM-killed at a 3 GB cap on
  # the 236 MB eastern zone. Ways crossing the bbox edge are cut, which is harmless here.
  run optiroute/osmium extract --strategy simple -b "$BBOX" \
    "/data/$(basename "$zone")" -o "/data/$REGION.osm.pbf.part" -f pbf --overwrite
  mv "$clip.part" "$clip"  # only a finished extract counts as done
fi

if [[ ! -f "$data/$REGION.osrm.done" ]]; then
  echo ">> osrm-extract (car)"
  run "$OSRM_IMAGE" osrm-extract -p /opt/car.lua -t "$CPUS" "/data/$REGION.osm.pbf"
  echo ">> osrm-partition"
  run "$OSRM_IMAGE" osrm-partition -t "$CPUS" "/data/$REGION.osrm"
  echo ">> osrm-customize"
  run "$OSRM_IMAGE" osrm-customize -t "$CPUS" "/data/$REGION.osrm"
  touch "$data/$REGION.osrm.done"  # set -e: only reached when all three steps succeeded
fi

# Dataset version, used in matrix cache keys: region + OSM data timestamp + OSRM image.
stamp=$(run optiroute/osmium fileinfo -g header.option.osmosis_replication_timestamp \
  "/data/$(basename "$zone")" 2>/dev/null || date -r "$zone" -u +%Y-%m-%dT%H:%M:%SZ)
echo "$REGION|$stamp|$OSRM_IMAGE" > "$data/DATASET_VERSION"
echo ">> done: $(cat "$data/DATASET_VERSION")"
du -sh "$data"
