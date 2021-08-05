package sysdb

func ReadDatabaseConnectors() ([]*DatabaseConnector, error) {
	mutex.Lock()
	defer mutex.Unlock()

	var cmap = make(map[string]map[string]string)
	var err error
	if cmap, err = readConfigMap("db"); err != nil {
		return nil, err
	}
	var dbc []*DatabaseConnector
	var name string
	var conf map[string]string
	for name, conf = range cmap {
		dbc = append(dbc, &DatabaseConnector{
			Name:       name,
			Type:       conf["type"],
			DBHost:     conf["host"],
			DBPort:     conf["port"],
			DBName:     conf["dbname"],
			DBUser:     conf["user"],
			DBPassword: conf["password"],
			DBSSLMode:  conf["sslmode"],
		})
	}
	return dbc, nil
}
