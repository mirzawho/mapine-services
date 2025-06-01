"use strict";

const axios = require('axios');

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

					const result = await axios({
						url: 'https://api.ohmyapi.com/v1/call/map.reverse.fromAddress',
						headers: {
							'x-api-key': process.env['OHMYAPI_API_KEY'],
							'x-cache': true
						},
						data: {
							language: 'فارسی',
							country: 'ایران',
							city,
							address,
						}
					});

					if (result.status == 200 && result.data['ok'] && result.data['locations']) {
						return result.data;
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
						axios({
							url: 'https://api.ohmyapi.com/v1/call/map.location.search',
							headers: {
								'x-api-key': process.env['OHMYAPI_API_KEY']
							},
							data: {
								category,
								lat: lon,
								lon: lat,
								zoom,
							}
						}),
						axios({
							url: 'https://api.ohmyapi.com/v1/call/map.reverse.fromLatLng',
							headers: {
								'x-api-key': process.env['OHMYAPI_API_KEY'],
								'x-cache': true
							},
							data: {
								language: 'فارسی',
								lat,
								lng: lon,
							}
						}),
					]);


					if (resultMapLocationSearch.status == 200 && resultMapLocationSearch.data['ok'] && resultMapLocationSearch.data['places']) {
						return {
							status: 200,
							meta: resultMapLocationSearch.data['meta'],
							places: resultMapLocationSearch.data['places'].map((item) => ({ id: item.id, ...item.data })),
							address: resultMapReverseFromLatLng.data['address'] || null
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
