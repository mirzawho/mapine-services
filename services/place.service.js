"use strict";

const { functions } = require('../shared');

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
				}
			},
			cache: {
				enabled: ctx => ctx.meta.cache,
				ttl: 60 * 5,
				keys: ['lat', 'lon', 'zoom', 'category'],
			},
			async handler(ctx) {
				try {
					const { lat, lon, zoom, category } = ctx.params;

					const [resultMapLocationSearch, resultMapReverseFromLatLng] = await Promise.all([
						functions.search(
							lon,
							lat,
							zoom,
							category
						),
						functions.fromLatLng(
							lat,
							lon
						),
					]);

					console.log(resultMapLocationSearch);

					if (resultMapLocationSearch.status == 200 && resultMapLocationSearch['places']) {
						return {
							status: 200,
							meta: resultMapLocationSearch['meta'],
							places: resultMapLocationSearch['places'].map((item) => ({ id: item.id, ...item })),
							address: resultMapReverseFromLatLng['address'] || null
						}
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
		}
	},
};
