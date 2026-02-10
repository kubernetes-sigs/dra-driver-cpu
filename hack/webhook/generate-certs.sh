#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="${1:-kube-system}"
SERVICE_NAME="${2:-dracpu-admission}"
SECRET_NAME="${3:-dracpu-admission-tls}"
WEBHOOK_NAME="${4:-dracpu-admission}"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

CA_CERT_FILE="${TMP_DIR}/ca.crt"
CA_KEY_FILE="${TMP_DIR}/ca.key"
CERT_FILE="${TMP_DIR}/tls.crt"
KEY_FILE="${TMP_DIR}/tls.key"
CSR_FILE="${TMP_DIR}/tls.csr"
EXT_FILE="${TMP_DIR}/tls.ext"

OPENSSL_SUBJ="/CN=${SERVICE_NAME}.${NAMESPACE}.svc"
OPENSSL_SAN="DNS:${SERVICE_NAME}.${NAMESPACE}.svc,DNS:${SERVICE_NAME}.${NAMESPACE}.svc.cluster.local"

openssl req -x509 -new -nodes \
	-keyout "${CA_KEY_FILE}" \
	-out "${CA_CERT_FILE}" \
	-days 3650 \
	-subj "/CN=${SERVICE_NAME}-ca" \
	-addext "basicConstraints=CA:TRUE" \
	-addext "keyUsage=critical,keyCertSign,cRLSign"

openssl req -new -newkey rsa:2048 -nodes \
	-keyout "${KEY_FILE}" \
	-out "${CSR_FILE}" \
	-subj "${OPENSSL_SUBJ}" \
	-addext "subjectAltName=${OPENSSL_SAN}"

cat >"${EXT_FILE}" <<EOF
subjectAltName=${OPENSSL_SAN}
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
EOF

openssl x509 -req -in "${CSR_FILE}" \
	-CA "${CA_CERT_FILE}" \
	-CAkey "${CA_KEY_FILE}" \
	-CAcreateserial \
	-out "${CERT_FILE}" \
	-days 3650 \
	-extfile "${EXT_FILE}"

kubectl -n "${NAMESPACE}" create secret tls "${SECRET_NAME}" \
	--cert="${CERT_FILE}" \
	--key="${KEY_FILE}" \
	--dry-run=client -o yaml | kubectl apply -f -

CA_BUNDLE="$(base64 -w0 <"${CA_CERT_FILE}")"
kubectl patch validatingwebhookconfiguration "${WEBHOOK_NAME}" \
	--type='json' \
	-p='[{"op":"replace","path":"/webhooks/0/clientConfig/caBundle","value":"'"${CA_BUNDLE}"'"}]'

echo "Updated ${WEBHOOK_NAME} caBundle and created ${SECRET_NAME} in ${NAMESPACE}."
