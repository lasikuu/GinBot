# Creating CA private key and self-signed certificate
openssl req -x509 -newkey rsa:4096 -nodes -days 365 -keyout ca-key.pem -out ca-cert.pem -subj "/C=JA/ST=ASIA/L=KYOTO/O=LASIKUU/OU=DEV/CN=*.lasikuu.jp/emailAddress="

echo "Self-signed certificate for CA"
openssl x509 -in ca-cert.pem -noout -text 

# ing Web Server private key and CSR
openssl req -newkey rsa:4096 -nodes -keyout server-key.pem -out server-req.pem -subj "/C=JA/ST=ASIA/L=KYOTO/O=LASIKUU/OU=DEV/CN=*.lasikuu.jp/emailAddress="

# Signing the Web Server Certificate Request (CSR)
openssl x509 -req -in server-req.pem -CA ca-cert.pem -CAkey ca-key.pem -CAcreateserial -out server-cert.pem -extfile server-ext.conf

echo "Signed certificate for server"
openssl x509 -in server-cert.pem -noout -text

echo "Verifying certificate"
openssl verify -CAfile ca-cert.pem server-cert.pem

# Generating client's private key and certificate signing request (CSR)
openssl req -newkey rsa:4096 -nodes -keyout client-key.pem -out client-req.pem -subj "/C=JA/ST=ASIA/L=KYOTO/O=LASIKUU/OU=DEV/CN=*.lasikuu.jp/emailAddress="

# Signing the Client Certificate Request (CSR)
openssl x509 -req -in client-req.pem -days 60 -CA ca-cert.pem -CAkey ca-key.pem -CAcreateserial -out client-cert.pem -extfile client-ext.conf

echo "Signed certificate for client"
openssl x509 -in client-cert.pem -noout -text

echo "Finished generating certificates"