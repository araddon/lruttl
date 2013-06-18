
Time Evict LRU
-------------------------

This is a modification of the Vitesse LRU to include Time Based Eviction

http://code.google.com/p/vitess/source/browse/go/cache


example::
	
	l := lruttl.NewTimeEvictLru(10000, 10*time.Minute, 1*time.Second)

	l.EvictCallback = func(key string, v lruttl.Value) {
		fmt.Println("evicting ", key)
	}

	for i := 0; i < 100; i++ {
		istr := strconv.FormatInt(int64(i), 10)
		l.Set("key"+istr, &CacheValue{i})
	}

