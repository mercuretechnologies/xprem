package bucket

func (v *validatingBucket) RetrieveMigrationHistory() ([]string, error) {
	return v.Inner.RetrieveMigrationHistory()
}

func (v *validatingBucket) ApplyMigration(migrationId string) error {
	if err := validateSegment("migrationId", migrationId); err != nil {
		return err
	}
	return v.Inner.ApplyMigration(migrationId)
}

func (v *validatingBucket) RemoveMigrationFromHistory(migrationId string) error {
	if err := validateSegment("migrationId", migrationId); err != nil {
		return err
	}
	return v.Inner.RemoveMigrationFromHistory(migrationId)
}
