# Backup & Restore

CCattler stores all cluster state in etcd. Backing up etcd is the primary mechanism for protecting cluster state.

## etcd snapshot

### Create a snapshot

```bash
etcdctl snapshot save /backup/ccattler-$(date +%Y%m%d-%H%M%S).db \
  --endpoints=https://127.0.0.1:2379 \
  --cert=/etc/ccattler/etcd-client.pem \
  --key=/etc/ccattler/etcd-client-key.pem \
  --cacert=/etc/ccattler/ca.pem
```

### Verify a snapshot

```bash
etcdctl snapshot status /backup/ccattler-20260918-120000.db --write-out=table
```

### Restore from snapshot

```bash
# Stop the server
sudo systemctl stop cca-server

# Restore etcd data
etcdctl snapshot restore /backup/ccattler-20260918-120000.db \
  --data-dir=/var/lib/etcd/restore

# Replace the data directory
sudo mv /var/lib/etcd/default.etcd /var/lib/etcd/default.etcd.bak
sudo mv /var/lib/etcd/restore /var/lib/etcd/default.etcd

# Start the server
sudo systemctl start cca-server
```

After restore, agents reconnect and re-report observed state. The reconciliation loop compares desired (from the restored snapshot) against current observed state and converges.

## Certificate backup

Back up the CA and node certificates:

```bash
tar czf ccattler-certs-$(date +%Y%m%d).tar.gz /etc/ccattler/*.pem
```

If certificates are lost, you'll need to re-enroll nodes with `cca join`.

## Configuration files

Back up your `.ccattler` configuration files separately — they are the source of truth for desired state. Store them in version control.

## Automated backups

Schedule etcd snapshots with a cron job:

```bash
# /etc/cron.d/ccattler-backup
0 */6 * * * root etcdctl snapshot save /backup/ccattler-$(date +\%Y\%m\%d-\%H\%M\%S).db --endpoints=https://127.0.0.1:2379 --cert=/etc/ccattler/etcd-client.pem --key=/etc/ccattler/etcd-client-key.pem --cacert=/etc/ccattler/ca.pem
```

Rotate old snapshots to avoid filling disk:

```bash
find /backup -name "ccattler-*.db" -mtime +7 -delete
```
