-- Encryption is per destination and remains off for existing configurations.
ALTER TABLE backup_destinations ADD COLUMN encrypted INTEGER NOT NULL DEFAULT 0 CHECK (encrypted IN (0, 1));
