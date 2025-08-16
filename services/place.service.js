"use strict";

const { functions, mongoose } = require('../shared');


/**
 * @typedef {import('moleculer').ServiceSchema} ServiceSchema Moleculer's Service Schema
 * @typedef {import('moleculer').Context} Context Moleculer's Context
 */

/** @type {ServiceSchema} */
module.exports = {
	name: "place",

	/**
	 * Actions
	 */
	actions: {
		search: {
			visibility: 'published',
			params: {
				address: {
					type: 'string',
					min: 3
				},
				city: {
					type: 'string',
					optional: true,
					default: null
				}
			},
			async handler(ctx) {
				try {
					const { address, city } = ctx.params;

					const result = await functions.fromAddress(address, city);

					if (result.status == 200 && result['locations']) {
						return result;
					}

					return {
						status: 502
					}
				} catch (error) {
					console.error(error);

					return {
						status: 500
					}
				}
			}
		},
		find: {
			visibility: 'published',
			params: {
				lat: {
					type: 'number',
					convert: true
				},
				lon: {
					type: 'number',
					convert: true
				},
				zoom: {
					type: 'number',
					convert: true,
					min: 1,
					max: 21
				},
				category: {
					type: 'enum',
					values: ['public-bathroom', 'mosque']
				},
				gps_lat: {
					type: 'number',
					convert: true,
					optional: true,
					default: 0,
				},
				gps_lon: {
					type: 'number',
					convert: true,
					optional: true,
					default: 0,
				},
			},
			cache: {
				enabled: ctx => ctx.meta.cache,
				ttl: 60 * 5,
				keys: ['lat', 'lon', 'zoom', 'category'],
			},
			async handler(ctx) {
				try {
					const { lat, lon, zoom, category, force, gps_lat, gps_lon } = ctx.params;

					var [resultMapLocationSearch, resultMapReverseFromLatLng] = await Promise.all([
						this.find(lat, lon, zoom, category),
						functions.fromLatLng(
							lat,
							lon
						),
					]);

					if (resultMapLocationSearch.length == 0 || force) {
						var resultMapLocationSearch = await functions.search(
							lon,
							lat,
							zoom,
							category
						);

						if (resultMapLocationSearch.status == 200 && resultMapLocationSearch['places']) {
							var places = resultMapLocationSearch['places'].map((item) => ({ osm_id: item.id, ...item['data'] }));

							ctx.emit('place.save', {
								places,
								category,
							});

							return {
								status: 200,
								meta: {
									offline: false,
									...resultMapLocationSearch['meta'],
								},
								places: places.map((item) => this.formatWithDistance(item, gps_lat, gps_lon)).sort((a, b) => a.distance - b.distance),
								address: resultMapReverseFromLatLng['address'] || null
							}
						}
					}

					return {
						status: 200,
						meta: {
							offline: true,
							total: resultMapLocationSearch.length,
						},
						places: resultMapLocationSearch.map((item) => this.formatWithDistance(item, gps_lat, gps_lon)).sort((a, b) => a.distance - b.distance),
						address: resultMapReverseFromLatLng['address'] || null
					}
				} catch (error) {
					console.error(error);

					return {
						status: 500
					}
				}
			}
		}
	},

	methods: {
		formatWithDistance(item, gps_lat, gps_lon) {
			var distance = gps_lat == 0 || gps_lon == 0 ? 0 : functions.calculateDistance(item.geometry.lon, item.geometry.lat, gps_lat, gps_lon);

			return {
				...item,
				distance,
			}
		},
		altitude(latitude, zoomlevel) {
			const EARTH_RADIUS = 6371;

			var C = Math.PI * 2 * EARTH_RADIUS;
			return (C * Math.cos(latitude) / Math.pow(2, zoomlevel)) + 2000;

		},
		async find(lat, lon, zoom, category) {
			try {
				const distance = this.altitude(lat, zoom);

				const result = await mongoose.collection('place').find({
					category,
					location: {
						$near: {
							$maxDistance: Math.abs(distance),
							$geometry: {
								type: "Point",
								coordinates: [lon, lat],
							},
						},
					}
				}).exec();

				console.log(`count ${result.length} at ${Math.round(distance)}m`);


				return result.map((item) => ({
					id: item._id,
					osm_id: item.osm_id,
					geometry: {
						lat: item.lon,
						lon: item.lat,
					},
					distance: 0,
					distance_unit: "km",
					category,
					name: item.title ?? '',
					address: item.address,
				}));
			} catch (error) {
				console.error(error);

				return [];
			}
		}
	},

	events: {
		async 'place.save'(ctx) {
			try {
				const places = ctx.params.places;
				const category = ctx.params.category;

				let saved = 0;

				for (const item of places) {
					var lat = item.geometry.lon;
					var lon = item.geometry.lat;

					var one = await mongoose.collection('place').findOne({
						lat,
						lon
					});

					if (one) {
						continue;
					}

					try {
						var result = await functions.fromLatLng(lat, lon, 16);

						var output = {
							osm_id: item.id || item.osm_id,
							category,
							location: {
								type: "Point",
								coordinates: [lon, lat],
							},
							lat: lat,
							lon: lon,
							title: item.name,
						};

						if (result.status == 200) {
							output = {
								...output,
								score: item.rating.score,
								address: result.address.address.replace(`${result.address.postcode},`, '').trim(),
								neighbourhood: result.address.neighbourhood,
								city: result.address.city?.replace('شهر', '').trim() ?? null,
								county: result.address.county?.replace('شهرستان', '').trim() ?? null,
								state: result.address.state?.replace('استان', '').trim() ?? null,
								country: result.address.country,
								title: item.name ?? null,
								description: null
							};
						}

						await mongoose.collection('place').insertOne(output);

						saved++;
					} catch (error) {
						//
					}
				}

				console.log(`Saved ${saved}/${places.length}`);

			} catch (error) {
				console.error(error);
			}
		}
	}
};
