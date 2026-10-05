#!/bin/sh
# Creates the private bucket and a scoped, non-root service credential that
# can only use objects in that bucket. Idempotent.
set -eu
: "${MINIO_ROOT_USER:?}" "${MINIO_ROOT_PASSWORD:?}" "${OBJECT_STORE_BUCKET:?}" "${OBJECT_STORE_ACCESS_KEY:?}" "${OBJECT_STORE_SECRET_KEY:?}"
export MC_CONFIG_DIR=/tmp/mc
# Credentials go through the environment, not the process command line.
# (Generated secrets are hex, so they are URL-safe.)
export MC_HOST_local="http://${MINIO_ROOT_USER}:${MINIO_ROOT_PASSWORD}@minio:9000"
mc mb --ignore-existing "local/${OBJECT_STORE_BUCKET}" >/dev/null
mc anonymous set none "local/${OBJECT_STORE_BUCKET}" >/dev/null
cat > /tmp/policy.json <<POLICY
{
  "Version": "2012-10-17",
  "Statement": [
    {"Effect": "Allow", "Action": ["s3:ListBucket", "s3:GetBucketLocation"], "Resource": ["arn:aws:s3:::${OBJECT_STORE_BUCKET}"]},
    {"Effect": "Allow", "Action": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"], "Resource": ["arn:aws:s3:::${OBJECT_STORE_BUCKET}/*"]}
  ]
}
POLICY
mc admin policy create local core-objects /tmp/policy.json >/dev/null
mc admin user add local "${OBJECT_STORE_ACCESS_KEY}" "${OBJECT_STORE_SECRET_KEY}" >/dev/null
mc admin policy attach local core-objects --user "${OBJECT_STORE_ACCESS_KEY}" >/dev/null 2>&1 || \
  mc admin user info local "${OBJECT_STORE_ACCESS_KEY}" | grep -q core-objects
echo "minio-init: bucket and scoped service credential ready"
